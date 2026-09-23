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
// compute, and the two services its credentials come from: Hanzo IAM, which
// mints the `hanzo-compute` app's client_credentials token, and STS, which
// exchanges that token for the `hanzo-compute` role. One httptest server answers
// all three; the SDK is pointed at it through its own endpoint variables and IAM
// through computeIamEndpoint.
//
// Each service checks what the real one would. IAM checks the client credential,
// form-encoded under Basic as RFC 6749 says. STS checks that the web identity is
// a token IAM minted and that it has not expired, and that the role is the
// configured one. EC2 accepts only calls signed with a key STS issued and that
// has not expired. The environment also carries a static AWS key, the way the
// visor pod carries one for its object store; a call signed with it is refused
// as AuthFailure, so a client that took it fails every test that reaches EC2.
// The instance metadata service is not served: a request for it is recorded as
// an "IMDS" call, which no test expects to see.
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

// The role and the IAM app it trusts. ClientSecret carries characters that
// only survive the token request if the credential is form-encoded.
const (
	RoleARN      = "arn:aws:iam::000000000000:role/hanzo-compute"
	ClientID     = "hanzo-compute"
	ClientSecret = "s3cr+t%/compute"
	// StaticKeyID sits in the environment and must never sign anything.
	StaticKeyID = "AKIASTORAGEKEY"
	// RoleKeyPrefix starts every access key STS issues for the role.
	RoleKeyPrefix = "ASIAHANZOCOMPUTE"
)

// Call is one request the fake answered: an EC2 action, AssumeRoleWithWebIdentity,
// "IAMToken" for a token mint, or "IMDS".
type Call struct {
	Action string
	Form   url.Values
	// KeyID is the access key the request was signed with; empty when unsigned.
	KeyID string
	// Client is the IAM client a token mint authenticated as.
	Client string
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

	// tokens are the IAM tokens minted and keys the role keys issued, each with
	// its expiry; tokenLife and keyLife are how long the next ones last.
	tokens    map[string]time.Time
	keys      map[string]time.Time
	tokenLife time.Duration
	keyLife   time.Duration
}

type image struct {
	arch       string
	root       string
	snapshotGB int
}

// New starts a fake account and returns it; Close stops it.
func New() (*Fake, func()) {
	f := &Fake{tokens: map[string]time.Time{}, keys: map[string]time.Time{}}
	f.reset()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = srv.URL
	return f, srv.Close
}

// Serve starts a fake account for one test and points the hosted account at it
// through the environment, the way a deployment configures it: the compute* keys
// (role, IAM client and IAM endpoint among them), the SDK's EC2 and STS endpoint
// variables, and a static key that must be ignored.
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
		"computeRegion":          Region,
		"computeSubnet":          Subnet,
		"computeSecurityGroup":   SecurityGroup,
		"computeImage":           Image,
		"computeGpuImage":        GPUImage,
		"computeRoleArn":         RoleARN,
		"computeIamClientId":     ClientID,
		"computeIamClientSecret": ClientSecret,
		"computeIamEndpoint":     f.URL,

		"AWS_ENDPOINT_URL_EC2": f.URL,
		"AWS_ENDPOINT_URL_STS": f.URL,
		// Were anything to ask the instance metadata service, it would ask here.
		"AWS_EC2_METADATA_SERVICE_ENDPOINT": f.URL,

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

// Reset empties the account and clears every recorded call and refusal. Tokens
// and role keys already issued stay valid, as a real session outlives what the
// account holds.
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
	f.tokenLife = time.Hour
	f.keyLife = time.Hour
}

// Lifetimes sets how long the IAM tokens and the role keys minted from now on
// last.
func (f *Fake) Lifetimes(token, key time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenLife, f.keyLife = token, key
}

// IsRoleKey reports whether id is an access key STS issued for the role.
func (f *Fake) IsRoleKey(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.keys[id]
	return ok
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

// SetState moves the instance id to state, as the cloud does on its own.
func (f *Fake) SetState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.instances {
		if in.ID == id {
			in.State = state
		}
	}
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
		f.mu.Lock()
		f.calls = append(f.calls, Call{Action: "IMDS"})
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/v1/iam/oauth/token" {
		f.mintToken(w, r)
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
	if until, ok := f.keys[call.KeyID]; !ok || !time.Now().Before(until) {
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

// mintToken is IAM's client_credentials grant: the client credential arrives
// form-encoded under Basic, and is decoded before it is compared.
func (f *Fake) mintToken(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	id, _ := url.QueryUnescape(user)
	secret, _ := url.QueryUnescape(pass)
	_ = r.ParseForm()

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Action: "IAMToken", Form: r.PostForm, Client: id})
	w.Header().Set("Content-Type", "application/json")
	if !ok || id != ClientID || secret != ClientSecret || r.PostForm.Get("grant_type") != "client_credentials" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_client","error_description":"client authentication failed"}`)
		return
	}
	f.next++
	token := fmt.Sprintf("iam-token-%d", f.next)
	f.tokens[token] = time.Now().Add(f.tokenLife)
	fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":%d}`, token, int(f.tokenLife/time.Second))
}

// assumeRole is STS's AssumeRoleWithWebIdentity: the web identity must be a live
// token IAM minted, and the role the configured one.
func (f *Fake) assumeRole(w http.ResponseWriter, call Call) {
	until, minted := f.tokens[call.Form.Get("WebIdentityToken")]
	switch {
	case !minted:
		stsError(w, "InvalidIdentityToken", "the web identity was not issued by the trusted provider")
		return
	case !time.Now().Before(until):
		stsError(w, "ExpiredTokenException", "the web identity has expired")
		return
	case call.Form.Get("RoleArn") != RoleARN:
		stsError(w, "AccessDenied", "not authorized to assume "+call.Form.Get("RoleArn"))
		return
	}
	f.next++
	key := fmt.Sprintf("%s%04d", RoleKeyPrefix, f.next)
	expires := time.Now().Add(f.keyLife)
	f.keys[key] = expires
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleWithWebIdentityResult>
<Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>role-secret</SecretAccessKey><SessionToken>role-session</SessionToken><Expiration>%s</Expiration></Credentials>
<SubjectFromWebIdentityToken>%s</SubjectFromWebIdentityToken>
<AssumedRoleUser><Arn>%s/%s</Arn><AssumedRoleId>AROA:%s</AssumedRoleId></AssumedRoleUser>
</AssumeRoleWithWebIdentityResult>
<ResponseMetadata><RequestId>sts-1</RequestId></ResponseMetadata>
</AssumeRoleWithWebIdentityResponse>`, key, expires.UTC().Format(time.RFC3339), ClientID, xmlText(RoleARN),
		xmlText(call.Form.Get("RoleSessionName")), xmlText(call.Form.Get("RoleSessionName")))
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
