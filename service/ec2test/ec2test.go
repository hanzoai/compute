// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

// Package ec2test fakes the hosted EC2 account for tests of visor's hosted
// compute: the EC2 query API it launches, lists, starts, stops and terminates
// instances through, the instance metadata service its instance role comes from,
// and the STS web-identity exchange IRSA uses. One httptest server answers all
// three, and the SDK is pointed at it through its own endpoint variables.
//
// The fake EC2 accepts only calls signed with a ROLE key — the one IMDS hands
// out or the one STS exchanges a web identity for. The environment also carries
// a static AWS key, the way the visor pod carries one for its object store; a
// call signed with it is refused as AuthFailure, so a client that took the
// static key fails every test that reaches EC2.
package ec2test

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The configured hosted account.
const (
	Region        = "us-east-1"
	Subnet        = "subnet-0hosted"
	SecurityGroup = "sg-0hosted"
	Image         = "ami-0cpu"
	GPUImage      = "ami-0gpu"
)

// Keys. RoleKeyID is what the instance role (IMDS) signs with and
// WebIdentityKeyID what the IRSA exchange yields; StaticKeyID sits in the
// environment and must never sign anything.
const (
	RoleKeyID        = "ASIAHOSTEDROLE"
	WebIdentityKeyID = "ASIAWEBIDENTITY"
	StaticKeyID      = "AKIASTORAGEKEY"
	RoleName         = "hanzo-compute-node"
	WebIdentityToken = "service-account-token"
)

// Call is one EC2 or STS request the fake answered.
type Call struct {
	Action string
	Form   url.Values
	// KeyID is the access key the request was signed with; empty when unsigned.
	KeyID string
}

// Instance is one instance in the fake account.
type Instance struct {
	ID          string
	Type        string
	Image       string
	State       string
	Tags        map[string]string
	LaunchTime  time.Time
	ClientToken string
	UserData    string
}

// Fake is the fake account. Its methods are safe for concurrent use.
type Fake struct {
	URL string

	mu        sync.Mutex
	calls     []Call
	instances []*Instance
	refuse    map[string]string
	images    map[string]image
	next      int
	now       time.Time
}

type image struct {
	arch       string
	root       string
	snapshotGB int
}

// New starts a fake account and returns it; Close stops it.
func New() (*Fake, func()) {
	f := &Fake{}
	f.reset()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = srv.URL
	return f, srv.Close
}

// Serve starts a fake account for one test and points the hosted account at it
// through the environment, the way a deployment configures it: the compute* keys,
// the SDK's EC2 and IMDS endpoint variables, and a static key that must be
// ignored.
func Serve(t testing.TB) *Fake {
	t.Helper()
	f, stop := New()
	t.Cleanup(stop)
	for k, v := range f.Env() {
		t.Setenv(k, v)
	}
	return f
}

// Env is the environment that points the hosted account at the fake.
func (f *Fake) Env() map[string]string {
	return map[string]string{
		"computeRegion":        Region,
		"computeSubnet":        Subnet,
		"computeSecurityGroup": SecurityGroup,
		"computeImage":         Image,
		"computeGpuImage":      GPUImage,

		"AWS_ENDPOINT_URL_EC2":              f.URL,
		"AWS_ENDPOINT_URL_STS":              f.URL,
		"AWS_EC2_METADATA_SERVICE_ENDPOINT": f.URL,
		"AWS_EC2_METADATA_DISABLED":         "false",

		// The pod's object-store key. It is in the environment and must sign nothing.
		"AWS_ACCESS_KEY_ID":     StaticKeyID,
		"AWS_SECRET_ACCESS_KEY": "storage-secret",

		// Nothing from a developer's own AWS setup reaches the test.
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             os.DevNull,
		"AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_ROLE_ARN":                "",
		"AWS_WEB_IDENTITY_TOKEN_FILE": "",
	}
}

// Reset empties the account and clears every recorded call and refusal.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reset()
}

func (f *Fake) reset() {
	f.calls = nil
	f.instances = nil
	f.refuse = map[string]string{}
	f.images = map[string]image{
		Image:    {arch: "x86_64", root: "/dev/sda1", snapshotGB: 8},
		GPUImage: {arch: "x86_64", root: "/dev/sda1", snapshotGB: 75},
	}
	f.next = 0
	f.now = time.Now().UTC().Truncate(time.Second)
}

// Refuse answers the next call of action with an EC2 error of code.
func (f *Fake) Refuse(action, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[action] = code
}

// SetImage replaces the description of an image.
func (f *Fake) SetImage(id, arch, root string, snapshotGB int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[id] = image{arch: arch, root: root, snapshotGB: snapshotGB}
}

// Add puts an instance in the account, as if something had launched it.
func (f *Fake) Add(in Instance) *Instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	if in.ID == "" {
		in.ID = fmt.Sprintf("i-%017d", f.next)
	}
	if in.LaunchTime.IsZero() {
		in.LaunchTime = f.now
	}
	if in.Tags == nil {
		in.Tags = map[string]string{}
	}
	f.instances = append(f.instances, &in)
	return &in
}

// Calls returns the calls answered so far, optionally only those of action.
func (f *Fake) Calls(action string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if action == "" || c.Action == action {
			out = append(out, c)
		}
	}
	return out
}

// Instances returns a copy of every instance in the account.
func (f *Fake) Instances() []Instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Instance, 0, len(f.instances))
	for _, in := range f.instances {
		out = append(out, *in)
	}
	return out
}

// ---- the server ----

var credentialRE = regexp.MustCompile(`Credential=([^/]+)/`)

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/latest/") {
		f.serveIMDS(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	call := Call{Action: r.PostForm.Get("Action"), Form: r.PostForm}
	if m := credentialRE.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		call.KeyID = m[1]
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)

	if call.Action == "AssumeRoleWithWebIdentity" {
		f.assumeRole(w, call)
		return
	}
	if call.KeyID != RoleKeyID && call.KeyID != WebIdentityKeyID {
		ec2Error(w, http.StatusUnauthorized, "AuthFailure", "request signed with "+strconv.Quote(call.KeyID))
		return
	}
	if code, ok := f.refuse[call.Action]; ok {
		delete(f.refuse, call.Action)
		ec2Error(w, http.StatusBadRequest, code, "refused by the test")
		return
	}
	switch call.Action {
	case "RunInstances":
		f.runInstances(w, call.Form)
	case "DescribeInstances":
		f.describeInstances(w, call.Form)
	case "DescribeImages":
		f.describeImages(w, call.Form)
	case "StartInstances":
		f.transition(w, call.Form, "StartInstancesResponse", "running")
	case "StopInstances":
		f.transition(w, call.Form, "StopInstancesResponse", "stopped")
	case "TerminateInstances":
		f.transition(w, call.Form, "TerminateInstancesResponse", "shutting-down")
	default:
		ec2Error(w, http.StatusBadRequest, "InvalidAction", call.Action)
	}
}

func (f *Fake) serveIMDS(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
		w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")
		fmt.Fprint(w, "imds-token")
	case r.Header.Get("X-Aws-Ec2-Metadata-Token") != "imds-token":
		http.Error(w, "IMDSv2 token required", http.StatusUnauthorized)
	case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
		fmt.Fprintln(w, RoleName)
	case r.URL.Path == "/latest/meta-data/iam/security-credentials/"+RoleName:
		fmt.Fprintf(w, `{"Code":"Success","Type":"AWS-HMAC","AccessKeyId":%q,"SecretAccessKey":"role-secret","Token":"role-session","Expiration":%q}`,
			RoleKeyID, time.Now().Add(6*time.Hour).UTC().Format(time.RFC3339))
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) assumeRole(w http.ResponseWriter, call Call) {
	if call.Form.Get("WebIdentityToken") != WebIdentityToken || call.Form.Get("RoleArn") == "" {
		stsError(w, "InvalidIdentityToken", "bad web identity")
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleWithWebIdentityResult>
<Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>web-secret</SecretAccessKey><SessionToken>web-session</SessionToken><Expiration>%s</Expiration></Credentials>
<SubjectFromWebIdentityToken>system:serviceaccount:hanzo:visor</SubjectFromWebIdentityToken>
<AssumedRoleUser><Arn>%s/visor</Arn><AssumedRoleId>AROA:visor</AssumedRoleId></AssumedRoleUser>
</AssumeRoleWithWebIdentityResult>
<ResponseMetadata><RequestId>sts-1</RequestId></ResponseMetadata>
</AssumeRoleWithWebIdentityResponse>`, WebIdentityKeyID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), xmlText(call.Form.Get("RoleArn")))
}

// indexed collects "prefix.N" form values in N order.
func indexed(form url.Values, prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v, ok := form[prefix+"."+strconv.Itoa(i)]
		if !ok {
			return out
		}
		out = append(out, v[0])
	}
}

// launchTags are the tags a RunInstances asked for on its instance.
func launchTags(form url.Values) map[string]string {
	tags := map[string]string{}
	for i := 1; ; i++ {
		p := "TagSpecification." + strconv.Itoa(i)
		rt, ok := form[p+".ResourceType"]
		if !ok {
			return tags
		}
		if rt[0] != "instance" {
			continue
		}
		for j := 1; ; j++ {
			k, ok := form[p+".Tag."+strconv.Itoa(j)+".Key"]
			if !ok {
				break
			}
			tags[k[0]] = form.Get(p + ".Tag." + strconv.Itoa(j) + ".Value")
		}
	}
}

func (f *Fake) runInstances(w http.ResponseWriter, form url.Values) {
	token := form.Get("ClientToken")
	var inst *Instance
	for _, in := range f.instances {
		if token != "" && in.ClientToken == token {
			inst = in
		}
	}
	if inst == nil {
		if _, ok := f.images[form.Get("ImageId")]; !ok {
			ec2Error(w, http.StatusBadRequest, "InvalidAMIID.NotFound", form.Get("ImageId"))
			return
		}
		f.next++
		inst = &Instance{
			ID:          fmt.Sprintf("i-%017d", f.next),
			Type:        form.Get("InstanceType"),
			Image:       form.Get("ImageId"),
			State:       "pending",
			Tags:        launchTags(form),
			LaunchTime:  f.now,
			ClientToken: token,
			UserData:    form.Get("UserData"),
		}
		f.instances = append(f.instances, inst)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RunInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>run-1</requestId><reservationId>r-1</reservationId><ownerId>000000000000</ownerId><instancesSet>%s</instancesSet></RunInstancesResponse>`,
		instanceXML(inst))
}

// matches reports whether in passes every Filter.N in form. A filter this fake
// does not know is an error, so a test cannot pass on a filter that was ignored.
func matches(in *Instance, form url.Values) (bool, error) {
	for i := 1; ; i++ {
		p := "Filter." + strconv.Itoa(i)
		name, ok := form[p+".Name"]
		if !ok {
			return true, nil
		}
		values := indexed(form, p+".Value")
		switch n := name[0]; {
		case n == "instance-state-name":
			if !slices.Contains(values, in.State) {
				return false, nil
			}
		case n == "tag-key":
			found := false
			for _, v := range values {
				if _, ok := in.Tags[v]; ok {
					found = true
				}
			}
			if !found {
				return false, nil
			}
		case strings.HasPrefix(n, "tag:"):
			v, ok := in.Tags[strings.TrimPrefix(n, "tag:")]
			if !ok || !slices.Contains(values, v) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unknown filter %s", n)
		}
	}
}

func (f *Fake) describeInstances(w http.ResponseWriter, form url.Values) {
	var b strings.Builder
	for _, in := range f.instances {
		ok, err := matches(in, form)
		if err != nil {
			ec2Error(w, http.StatusBadRequest, "InvalidParameterValue", err.Error())
			return
		}
		if ok {
			fmt.Fprintf(&b, `<item><reservationId>r-%s</reservationId><ownerId>000000000000</ownerId><instancesSet>%s</instancesSet></item>`, in.ID, instanceXML(in))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>describe-1</requestId><reservationSet>%s</reservationSet></DescribeInstancesResponse>`, b.String())
}

func (f *Fake) describeImages(w http.ResponseWriter, form url.Values) {
	var b strings.Builder
	for _, id := range indexed(form, "ImageId") {
		img, ok := f.images[id]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, `<item><imageId>%s</imageId><architecture>%s</architecture><rootDeviceName>%s</rootDeviceName><rootDeviceType>ebs</rootDeviceType><blockDeviceMapping><item><deviceName>%s</deviceName><ebs><snapshotId>snap-1</snapshotId><volumeSize>%d</volumeSize></ebs></item></blockDeviceMapping></item>`,
			xmlText(id), img.arch, img.root, img.root, img.snapshotGB)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>images-1</requestId><imagesSet>%s</imagesSet></DescribeImagesResponse>`, b.String())
}

func (f *Fake) transition(w http.ResponseWriter, form url.Values, response, state string) {
	var b strings.Builder
	for _, id := range indexed(form, "InstanceId") {
		for _, in := range f.instances {
			if in.ID != id {
				continue
			}
			prev := in.State
			in.State = state
			if state == "running" {
				in.LaunchTime = f.now
			}
			fmt.Fprintf(&b, `<item><instanceId>%s</instanceId><currentState><code>0</code><name>%s</name></currentState><previousState><code>0</code><name>%s</name></previousState></item>`, id, state, prev)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>state-1</requestId><instancesSet>%s</instancesSet></%s>`, response, b.String(), response)
}

func instanceXML(in *Instance) string {
	var tags strings.Builder
	keys := make([]string, 0, len(in.Tags))
	for k := range in.Tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&tags, `<item><key>%s</key><value>%s</value></item>`, xmlText(k), xmlText(in.Tags[k]))
	}
	return fmt.Sprintf(`<item><instanceId>%s</instanceId><imageId>%s</imageId><instanceState><code>0</code><name>%s</name></instanceState><instanceType>%s</instanceType><launchTime>%s</launchTime><placement><availabilityZone>%sa</availabilityZone></placement><privateIpAddress>10.0.0.10</privateIpAddress><ipAddress>203.0.113.10</ipAddress><tagSet>%s</tagSet></item>`,
		in.ID, xmlText(in.Image), in.State, xmlText(in.Type), in.LaunchTime.UTC().Format(time.RFC3339), Region, tags.String())
}

func ec2Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>err-1</RequestID></Response>`, code, xmlText(msg))
}

func stsError(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>sts-err</RequestId></ErrorResponse>`, code, xmlText(msg))
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
