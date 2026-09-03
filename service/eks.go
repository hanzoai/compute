// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/hanzoai/visor/logs"
)

// providerAWS is AWS's name as NewMachineClient spells it.
const providerAWS = "AWS"

// The two IAM roles every EKS cluster needs, ensured once per account: the one
// the control plane assumes and the one its worker instances run as.
const (
	eksClusterRole = "hanzo-visor-eks-cluster"
	eksNodeRole    = "hanzo-visor-eks-node"

	// eksClusterHeader names the cluster inside the presigned STS request, which
	// is how the cluster's authenticator ties the token to itself.
	eksClusterHeader = "x-k8s-aws-id"
	eksTokenPrefix   = "k8s-aws-v1."
	// eksTokenLife is a little under the fifteen minutes the authenticator honours
	// for a presigned request, whatever expiry the URL itself states.
	eksTokenLife = 14 * time.Minute
	// eksActiveWait bounds how long a create waits for the control plane before
	// seeding the first nodegroup. EKS takes ten minutes or so.
	eksActiveWait = 20 * time.Minute
)

var (
	eksClusterPolicies = []string{"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"}
	eksNodePolicies    = []string{
		"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
		"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
		"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
	}
)

// trust is the assume-role document letting one AWS service take a role.
func trust(service string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` +
		service + `"},"Action":"sts:AssumeRole"}]}`
}

// The four AWS faces EKS work touches, each as the subset actually called, so a
// test hands in a fake and no test needs a key.
type eksAPI interface {
	eks.DescribeClusterAPIClient
	eks.DescribeNodegroupAPIClient
	eks.ListClustersAPIClient
	eks.ListNodegroupsAPIClient
	CreateCluster(context.Context, *eks.CreateClusterInput, ...func(*eks.Options)) (*eks.CreateClusterOutput, error)
	DeleteCluster(context.Context, *eks.DeleteClusterInput, ...func(*eks.Options)) (*eks.DeleteClusterOutput, error)
	CreateNodegroup(context.Context, *eks.CreateNodegroupInput, ...func(*eks.Options)) (*eks.CreateNodegroupOutput, error)
	UpdateNodegroupConfig(context.Context, *eks.UpdateNodegroupConfigInput, ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
	DeleteNodegroup(context.Context, *eks.DeleteNodegroupInput, ...func(*eks.Options)) (*eks.DeleteNodegroupOutput, error)
}

type iamAPI interface {
	CreateRole(context.Context, *iam.CreateRoleInput, ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	AttachRolePolicy(context.Context, *iam.AttachRolePolicyInput, ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
}

type ec2API interface {
	ec2.DescribeInstancesAPIClient
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
}

type stsPresigner interface {
	PresignGetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// EKSClient is AWS's managed-Kubernetes face. Clusters are EKS clusters and pools
// are managed nodegroups; nodes are the EC2 instances a nodegroup tags with the
// cluster's name.
type EKSClient struct {
	eks    eksAPI
	iam    iamAPI
	ec2    ec2API
	sts    stsPresigner
	region string
	// carried is true when the key is not in this process. Every API call still
	// works (egress signs); minting an apiserver token does not, because a
	// presigned URL is signed here or not at all.
	carried bool
	// wait overrides eksActiveWait; zero means the default.
	wait time.Duration
}

func (c *EKSClient) Provider() string { return providerAWS }

// ---- pure mappers ----

func clusterFromEKS(cl *ekstypes.Cluster, region string) *KubernetesCluster {
	return &KubernetesCluster{
		ID:         aws.ToString(cl.Name),
		Name:       aws.ToString(cl.Name),
		RegionSlug: region,
		Status:     string(cl.Status),
		Tags:       tagList(cl.Tags),
		Provider:   providerAWS,
	}
}

// poolFromNodegroup maps a managed nodegroup. EKS has no autoscale flag — the
// Cluster Autoscaler is an add-on that moves desired between the bounds — so a
// pool whose bounds differ is one that may be scaled.
func poolFromNodegroup(ng *ekstypes.Nodegroup) *NodePool {
	np := &NodePool{
		ID:     aws.ToString(ng.NodegroupName),
		Name:   aws.ToString(ng.NodegroupName),
		Labels: ng.Labels,
		Tags:   tagList(ng.Tags),
	}
	if len(ng.InstanceTypes) > 0 {
		np.Size = ng.InstanceTypes[0]
	}
	applyScaling(np, ng.ScalingConfig)
	return np
}

func applyScaling(np *NodePool, s *ekstypes.NodegroupScalingConfig) {
	if s == nil {
		return
	}
	np.Count = int(aws.ToInt32(s.DesiredSize))
	np.MinNodes = int(aws.ToInt32(s.MinSize))
	np.MaxNodes = int(aws.ToInt32(s.MaxSize))
	np.AutoScale = np.MinNodes != np.MaxNodes
}

// buildEKSCluster is the spec→CreateCluster contract. Authentication is API and
// config-map both, so the creating identity can reach the apiserver through the
// access API without a hand-edited aws-auth map.
func buildEKSCluster(spec *CreateClusterSpec, tags []string, roleArn string, subnets []string) *eks.CreateClusterInput {
	in := &eks.CreateClusterInput{
		Name:               aws.String(spec.Name),
		RoleArn:            aws.String(roleArn),
		ResourcesVpcConfig: &ekstypes.VpcConfigRequest{SubnetIds: subnets},
		AccessConfig: &ekstypes.CreateAccessConfigRequest{
			AuthenticationMode: ekstypes.AuthenticationModeApiAndConfigMap,
		},
		Tags: tagMap(tags),
	}
	if v := strings.TrimSpace(spec.Version); v != "" {
		in.Version = aws.String(v)
	}
	return in
}

func buildNodegroup(cluster string, spec *CreateNodePoolSpec, nodeRole string, subnets []string) *eks.CreateNodegroupInput {
	return &eks.CreateNodegroupInput{
		ClusterName:   aws.String(cluster),
		NodegroupName: aws.String(spec.Name),
		NodeRole:      aws.String(nodeRole),
		Subnets:       subnets,
		InstanceTypes: []string{spec.Size},
		ScalingConfig: scalingFor(spec),
		Labels:        spec.Labels,
		Tags:          tagMap(spec.Tags),
	}
}

// scalingFor pins a fixed pool at its count and starts an autoscaled one at its
// count inside its bounds. EKS refuses a maximum under one.
func scalingFor(spec *CreateNodePoolSpec) *ekstypes.NodegroupScalingConfig {
	n := int32(spec.Count)
	lo, hi := n, max(n, 1)
	if spec.AutoScale {
		lo, hi = int32(spec.MinNodes), max(int32(spec.MaxNodes), 1)
		n = min(max(n, lo), hi)
	}
	return &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(lo), MaxSize: aws.Int32(hi), DesiredSize: aws.Int32(n)}
}

// scaled moves a pool to count. A fixed pool (bounds equal) stays fixed at the
// new count; an autoscaled one keeps its bounds, widened only as far as count
// needs — EKS refuses a desired size outside them, and a scale is an instruction.
func scaled(cur *ekstypes.NodegroupScalingConfig, count int) *ekstypes.NodegroupScalingConfig {
	n := int32(count)
	lo, hi := n, max(n, 1)
	if cur != nil && aws.ToInt32(cur.MinSize) != aws.ToInt32(cur.MaxSize) {
		lo, hi = min(aws.ToInt32(cur.MinSize), n), max(aws.ToInt32(cur.MaxSize), n)
	}
	return &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(lo), MaxSize: aws.Int32(hi), DesiredSize: aws.Int32(n)}
}

// eksToken encodes a presigned sts:GetCallerIdentity the way the cluster's
// authenticator reads it: prefix, then the URL base64url without padding.
func eksToken(presignedURL string) string {
	return eksTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(presignedURL))
}

// ---- account prerequisites ----

// ensureRole creates a role and attaches its policies, tolerating a role that
// already exists and a policy already attached: both answers say the account is
// as required, which is what the caller asked.
func (c *EKSClient) ensureRole(ctx context.Context, name, service string, policies []string) (string, error) {
	var arn string
	out, err := c.iam.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(trust(service)),
	})
	var exists *iamtypes.EntityAlreadyExistsException
	switch {
	case err == nil:
		arn = aws.ToString(out.Role.Arn)
	case errors.As(err, &exists):
		got, err := c.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
		if err != nil {
			return "", fmt.Errorf("iam: read role %s: %w", name, err)
		}
		arn = aws.ToString(got.Role.Arn)
	default:
		return "", fmt.Errorf("iam: create role %s: %w", name, err)
	}
	for _, p := range policies {
		if _, err := c.iam.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(p)}); err != nil {
			return "", fmt.Errorf("iam: attach %s to %s: %w", p, name, err)
		}
	}
	return arn, nil
}

// subnets is where the cluster goes: what the spec named, else the default VPC's
// one-per-zone subnets. EKS wants at least two zones.
func (c *EKSClient) subnets(ctx context.Context, want []string) ([]string, error) {
	if len(want) > 0 {
		return want, nil
	}
	out, err := c.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}},
	})
	if err != nil {
		return nil, fmt.Errorf("ec2: list default subnets: %w", err)
	}
	ids := make([]string, 0, len(out.Subnets))
	for _, s := range out.Subnets {
		ids = append(ids, aws.ToString(s.SubnetId))
	}
	if len(ids) < 2 {
		return nil, fmt.Errorf("eks needs subnets in two zones and the default VPC has %d: name subnetIds", len(ids))
	}
	return ids, nil
}

// ---- clusters ----

func (c *EKSClient) ListClusters(ctx context.Context) ([]*KubernetesCluster, error) {
	var out []*KubernetesCluster
	p := eks.NewListClustersPaginator(c.eks, &eks.ListClustersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("eks: list clusters: %w", err)
		}
		for _, name := range page.Clusters {
			d, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
			if err != nil {
				return nil, fmt.Errorf("eks: describe cluster %s: %w", name, err)
			}
			out = append(out, clusterFromEKS(d.Cluster, c.region))
		}
	}
	return out, nil
}

func (c *EKSClient) GetCluster(ctx context.Context, id string) (*KubernetesClusterDetail, error) {
	d, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(id)})
	if err != nil {
		return nil, fmt.Errorf("eks: describe cluster %s: %w", id, err)
	}
	pools, err := c.pools(ctx, id)
	if err != nil {
		return nil, err
	}
	nodes, err := c.nodes(ctx, id)
	if err != nil {
		return nil, err
	}
	return &KubernetesClusterDetail{KubernetesCluster: *clusterFromEKS(d.Cluster, c.region), NodePools: pools, Nodes: nodes}, nil
}

func (c *EKSClient) nodegroupNames(ctx context.Context, cluster string) ([]string, error) {
	var names []string
	p := eks.NewListNodegroupsPaginator(c.eks, &eks.ListNodegroupsInput{ClusterName: aws.String(cluster)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("eks: list nodegroups of %s: %w", cluster, err)
		}
		names = append(names, page.Nodegroups...)
	}
	return names, nil
}

func (c *EKSClient) pools(ctx context.Context, cluster string) ([]*NodePool, error) {
	names, err := c.nodegroupNames(ctx, cluster)
	if err != nil {
		return nil, err
	}
	pools := make([]*NodePool, 0, len(names))
	for _, n := range names {
		d, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(n)})
		if err != nil {
			return nil, fmt.Errorf("eks: describe nodegroup %s/%s: %w", cluster, n, err)
		}
		pools = append(pools, poolFromNodegroup(d.Nodegroup))
	}
	return pools, nil
}

// nodes are the instances EKS tags with the cluster's name — one Machine each,
// the shape the fleet lists a standalone instance in.
func (c *EKSClient) nodes(ctx context.Context, cluster string) ([]*Machine, error) {
	var out []*Machine
	p := ec2.NewDescribeInstancesPaginator(c.ec2, &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:eks:cluster-name"), Values: []string{cluster}},
		{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
	}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ec2: list nodes of %s: %w", cluster, err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				m := getMachineFromAwsInstance(i)
				m.Provider = providerAWS
				m.Tag = "eks-cluster:" + cluster
				m.PublicIp = aws.ToString(i.PublicIpAddress)
				m.PrivateIp = aws.ToString(i.PrivateIpAddress)
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// NodeMachines is every worker on every cluster this account has.
func (c *EKSClient) NodeMachines(ctx context.Context) ([]*Machine, error) {
	clusters, err := c.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Machine
	for _, cl := range clusters {
		ns, err := c.nodes(ctx, cl.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ns...)
	}
	return out, nil
}

// CreateCluster makes the control plane and returns it CREATING. A nodegroup
// cannot be created until the cluster is ACTIVE, which takes EKS ten minutes or
// so, so the seed pool follows on its own once it can; the billing row was
// written from the same spec, so the pool bills either way, and a seed that
// fails is the loudest line here because nothing else reconciles it.
func (c *EKSClient) CreateCluster(ctx context.Context, spec *CreateClusterSpec, tags []string) (*KubernetesCluster, error) {
	roleArn, err := c.ensureRole(ctx, eksClusterRole, "eks.amazonaws.com", eksClusterPolicies)
	if err != nil {
		return nil, err
	}
	subnets, err := c.subnets(ctx, spec.SubnetIDs)
	if err != nil {
		return nil, err
	}
	out, err := c.eks.CreateCluster(ctx, buildEKSCluster(spec, tags, roleArn, subnets))
	if err != nil {
		return nil, fmt.Errorf("eks: create cluster %s: %w", spec.Name, err)
	}
	go func() {
		if err := c.seed(context.Background(), spec, tags); err != nil {
			logs.Error("eks: cluster %s is up but its seed pool %s was not created: %v", spec.Name, seedPoolName(spec), err)
		}
	}()
	return clusterFromEKS(out.Cluster, c.region), nil
}

// seed waits for the control plane and creates the cluster's first pool.
func (c *EKSClient) seed(ctx context.Context, spec *CreateClusterSpec, tags []string) error {
	wait := c.wait
	if wait == 0 {
		wait = eksActiveWait
	}
	if err := eks.NewClusterActiveWaiter(c.eks).Wait(ctx, &eks.DescribeClusterInput{Name: aws.String(spec.Name)}, wait); err != nil {
		return fmt.Errorf("eks: cluster %s did not become active: %w", spec.Name, err)
	}
	_, err := c.CreateNodePool(ctx, spec.Name, &CreateNodePoolSpec{
		Name: seedPoolName(spec), Size: spec.NodePool.Size, Count: seedPoolCount(spec), Tags: tags,
	})
	return err
}

// DeleteCluster removes the nodegroups, then the cluster. EKS refuses to delete
// a cluster that still has one, and a nodegroup takes minutes to go, so with
// pools present the cluster's own delete follows once they are gone.
func (c *EKSClient) DeleteCluster(ctx context.Context, id string) error {
	names, err := c.nodegroupNames(ctx, id)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, err := c.eks.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: aws.String(id), NodegroupName: aws.String(n)}); err != nil && !IsNotFound(err) {
			return fmt.Errorf("eks: delete nodegroup %s/%s: %w", id, n, err)
		}
	}
	if len(names) == 0 {
		return c.drop(ctx, id)
	}
	go func() {
		if err := c.dropAfter(context.Background(), id, names); err != nil {
			logs.Error("eks: cluster %s: its nodegroups were deleted but the cluster was not: %v", id, err)
		}
	}()
	return nil
}

func (c *EKSClient) drop(ctx context.Context, id string) error {
	if _, err := c.eks.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: aws.String(id)}); err != nil {
		return fmt.Errorf("eks: delete cluster %s: %w", id, err)
	}
	return nil
}

func (c *EKSClient) dropAfter(ctx context.Context, id string, names []string) error {
	wait := c.wait
	if wait == 0 {
		wait = eksActiveWait
	}
	for _, n := range names {
		err := eks.NewNodegroupDeletedWaiter(c.eks).Wait(ctx,
			&eks.DescribeNodegroupInput{ClusterName: aws.String(id), NodegroupName: aws.String(n)}, wait)
		if err != nil {
			return fmt.Errorf("eks: nodegroup %s/%s did not delete: %w", id, n, err)
		}
	}
	return c.drop(ctx, id)
}

// ---- pools ----

func (c *EKSClient) CreateNodePool(ctx context.Context, clusterID string, spec *CreateNodePoolSpec) (*NodePool, error) {
	nodeRole, err := c.ensureRole(ctx, eksNodeRole, "ec2.amazonaws.com", eksNodePolicies)
	if err != nil {
		return nil, err
	}
	d, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(clusterID)})
	if err != nil {
		return nil, fmt.Errorf("eks: describe cluster %s: %w", clusterID, err)
	}
	var subnets []string
	if d.Cluster.ResourcesVpcConfig != nil {
		subnets = d.Cluster.ResourcesVpcConfig.SubnetIds
	}
	out, err := c.eks.CreateNodegroup(ctx, buildNodegroup(clusterID, spec, nodeRole, subnets))
	if err != nil {
		return nil, fmt.Errorf("eks: create nodegroup %s/%s: %w", clusterID, spec.Name, err)
	}
	return poolFromNodegroup(out.Nodegroup), nil
}

// ScaleNodePool sets the pool's size. The update is asynchronous upstream, so the
// pool returned carries the scaling asked for rather than the one still showing.
func (c *EKSClient) ScaleNodePool(ctx context.Context, clusterID, poolID string, count int) (*NodePool, error) {
	ref := &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterID), NodegroupName: aws.String(poolID)}
	d, err := c.eks.DescribeNodegroup(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("eks: describe nodegroup %s/%s: %w", clusterID, poolID, err)
	}
	next := scaled(d.Nodegroup.ScalingConfig, count)
	_, err = c.eks.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
		ClusterName: ref.ClusterName, NodegroupName: ref.NodegroupName, ScalingConfig: next,
	})
	if err != nil {
		return nil, fmt.Errorf("eks: scale nodegroup %s/%s: %w", clusterID, poolID, err)
	}
	np := poolFromNodegroup(d.Nodegroup)
	applyScaling(np, next)
	return np, nil
}

func (c *EKSClient) DeleteNodePool(ctx context.Context, clusterID, poolID string) error {
	if _, err := c.eks.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: aws.String(clusterID), NodegroupName: aws.String(poolID)}); err != nil {
		return fmt.Errorf("eks: delete nodegroup %s/%s: %w", clusterID, poolID, err)
	}
	return nil
}

// ---- credentials ----

// GetCredentials is what `aws eks get-token` hands kubectl: the cluster's
// endpoint and CA from DescribeCluster, and a presigned sts:GetCallerIdentity
// carrying the cluster's name as a signed header, which the cluster's
// authenticator replays to learn who is calling.
func (c *EKSClient) GetCredentials(ctx context.Context, clusterID string) (*ClusterCredentials, error) {
	if c.carried {
		return nil, fmt.Errorf("eks: a carried account cannot mint an apiserver token: the presigned STS request is signed with the key, and the key is not here")
	}
	d, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(clusterID)})
	if err != nil {
		return nil, fmt.Errorf("eks: describe cluster %s: %w", clusterID, err)
	}
	if d.Cluster.CertificateAuthority == nil || d.Cluster.Endpoint == nil {
		return nil, fmt.Errorf("eks: cluster %s has no endpoint yet (%s)", clusterID, d.Cluster.Status)
	}
	ca, err := base64.StdEncoding.DecodeString(aws.ToString(d.Cluster.CertificateAuthority.Data))
	if err != nil {
		return nil, fmt.Errorf("eks: cluster %s certificate authority: %w", clusterID, err)
	}
	token, expiry, err := c.token(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return &ClusterCredentials{Endpoint: aws.ToString(d.Cluster.Endpoint), CAData: ca, Token: token, Expiry: expiry}, nil
}

func (c *EKSClient) token(ctx context.Context, cluster string) (string, time.Time, error) {
	req, err := c.sts.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(po *sts.PresignOptions) {
		po.ClientOptions = append(po.ClientOptions, func(o *sts.Options) {
			o.APIOptions = append(o.APIOptions,
				smithyhttp.SetHeaderValue(eksClusterHeader, cluster),
				smithyhttp.SetHeaderValue("X-Amz-Expires", "60"),
			)
		})
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sts: presign for cluster %s: %w", cluster, err)
	}
	return eksToken(req.URL), time.Now().Add(eksTokenLife), nil
}
