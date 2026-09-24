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

// Package ec2test fakes the hosted EC2 account as compute reaches it: through
// hanzoai/egress. It is a stand-in egress on a real ZAP listener answering
// POST /v1/fetch, with EC2's Query API behind it.
//
// It checks each fetch the way egress would before signing it: the caller's own
// bearer, the AWS account hanzo-compute, the endpoint ec2.<region>.amazonaws.com
// (or CloudWatch's, for GetMetricData), a form POST to "/". And it checks what egress could not: that the request
// compute described carries no signature of its own — no Authorization, no
// X-Amz-* query or form parameter — so nothing in compute signed it. A fetch
// that fails a check is refused as egress refuses, which the SDK sees as
// spend.ErrRefused, and is recorded as a "Refused" call.
package ec2test

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/egress/spend"
	"github.com/zap-proto/zip"
)

// The configured hosted account: Hanzo's, in us-east-1.
const (
	Region        = "us-east-1"
	Subnet        = "subnet-0984e347d9bb3e01b"
	SecurityGroup = "sg-00478a75c50869398"
	Image         = "ami-0045d7fc2ad003464"
	GPUImage      = "ami-0c20dc14952c0c073"
	// Endpoint is the host every hosted EC2 call must be addressed to.
	Endpoint = "ec2." + Region + ".amazonaws.com"
	// Monitoring is CloudWatch's host, where the meter reads NetworkOut.
	Monitoring = "monitoring." + Region + ".amazonaws.com"
	// Account is the label of the account's descriptor in egress's custody.
	Account = "hanzo-compute"
	// Token is compute's own IAM token, the only thing it shows egress.
	Token = "compute-own-iam-token"
	// StaticKeyID sits in the environment, as the pod's object-store key does,
	// and must never sign anything.
	StaticKeyID = "AKIASTORAGEKEY"
)

// Call is one fetch the fake answered: the EC2 action it carried, or "Refused"
// with the reason egress would have given.
type Call struct {
	Action  string
	Form    url.Values
	Fetch   spend.Fetch
	Bearer  string
	Refused string
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

// Fake is the fake account and the egress in front of it. Its methods are safe
// for concurrent use.
type Fake struct {
	// Address is where the stand-in egress listens, host:port over ZAP.
	Address string

	mu        sync.Mutex
	calls     []Call
	instances []*Instance
	refuse    map[string]string
	images    map[string]image
	next      int
	now       time.Time
	deny      string
	// sent is NetworkOut: bytes by instance id and hour ("YYYYMMDDHH").
	sent map[string]map[string]int64
	// uncounted is the StatusCode CloudWatch answers for an instance it cannot
	// count, by instance id.
	uncounted map[string]string
}

type image struct {
	arch       string
	root       string
	snapshotGB int
}

// New starts a fake account behind a stand-in egress and returns it; the func
// stops it.
func New() (*Fake, func()) {
	f := &Fake{}
	f.reset()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	f.Address = l.Addr().String()
	_ = l.Close()

	app := zip.New(zip.Config{AppName: "egress"})
	app.Post("/v1/fetch", f.fetch)
	go func() { _ = app.Listen(f.Address) }()
	for range 500 {
		if c, err := net.DialTimeout("tcp", f.Address, time.Second); err == nil {
			_ = c.Close()
			return f, func() { _ = app.Shutdown() }
		}
		time.Sleep(10 * time.Millisecond)
	}
	panic("ec2test: the stand-in egress never bound " + f.Address)
}

// Serve starts a fake account for one test and configures the hosted account
// through the environment the way a deployment does. The caller registers the
// carrier (Client) — this package cannot, as it sits below the service that
// owns it.
func Serve(t testing.TB) *Fake {
	t.Helper()
	f, stop := New()
	t.Cleanup(stop)
	for k, v := range f.Env() {
		t.Setenv(k, v)
	}
	return f
}

// Client is the carrier's http.Client for one account, as compute's carry()
// builds it: spend.Client to this egress, presenting Token.
func (f *Fake) Client(provider, account string) *http.Client {
	return spend.Client(spend.Config{
		Network: "tcp", Address: f.Address, Token: Token,
		Provider: provider, Account: account,
	})
}

// Env is the environment that configures the hosted account, and what a pod
// also carries that must change nothing: an object-store key, and endpoint and
// credential settings the AWS SDK would read if it were allowed to.
func (f *Fake) Env() map[string]string {
	return map[string]string{
		"computeRegion":        Region,
		"computeSubnet":        Subnet,
		"computeSecurityGroup": SecurityGroup,
		"computeImage":         Image,
		"computeGpuImage":      GPUImage,

		"AWS_ACCESS_KEY_ID":           StaticKeyID,
		"AWS_SECRET_ACCESS_KEY":       "storage-secret",
		"AWS_SESSION_TOKEN":           "storage-session",
		"AWS_REGION":                  "eu-west-1",
		"AWS_ENDPOINT_URL":            "https://aws.elsewhere.example",
		"AWS_ENDPOINT_URL_EC2":        "https://ec2.elsewhere.example",
		"AWS_ROLE_ARN":                "arn:aws:iam::000000000000:role/somebody-else",
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             os.DevNull,
		"AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
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
	f.deny = ""
	f.sent = map[string]map[string]int64{}
	f.uncounted = map[string]string{}
}

// Send records that instance id sent bytes out during the hour at falls in, as
// CloudWatch's NetworkOut would.
func (f *Fake) Send(id string, at time.Time, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hour := at.UTC().Format("2006010215")
	if f.sent[id] == nil {
		f.sent[id] = map[string]int64{}
	}
	f.sent[id][hour] += bytes
}

// Uncounted makes CloudWatch answer status (InternalError, Forbidden) for
// instance id's NetworkOut from now until Reset.
func (f *Fake) Uncounted(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uncounted[id] = status
}

// Deny makes the stand-in egress refuse every call from now until Reset, as
// egress does when the account's role cannot be assumed.
func (f *Fake) Deny(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deny = reason
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

// ---- the stand-in egress ----

// fetch is POST /v1/fetch: one described request, checked, answered by the
// account behind it, and returned the way egress returns an answer that is not
// JSON — AWS's own bytes and Content-Type.
func (f *Fake) fetch(c *zip.Ctx) error {
	var in spend.Fetch
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("not a fetch")
	}
	form, _ := url.ParseQuery(string(in.Raw))
	call := Call{Action: form.Get("Action"), Form: form, Fetch: in, Bearer: c.Header("Authorization")}

	f.mu.Lock()
	defer f.mu.Unlock()
	if why := f.check(in, call.Bearer, form); why != "" {
		call.Action, call.Refused = "Refused", why
		f.calls = append(f.calls, call)
		return zip.Errorf(http.StatusForbidden, "%s", why)
	}
	f.calls = append(f.calls, call)

	rec := httptest.NewRecorder()
	f.answer(rec, call)
	return c.JSON(http.StatusOK, spend.Fetched{
		Status: rec.Code,
		Raw:    rec.Body.Bytes(),
		Type:   rec.Header().Get("Content-Type"),
		Scope:  "org",
	})
}

// check is what egress requires of a hosted EC2 fetch, and that the request
// arrived unsigned.
func (f *Fake) check(in spend.Fetch, bearer string, form url.Values) string {
	switch {
	case f.deny != "":
		return f.deny
	case bearer != "Bearer "+Token:
		return "the caller is not compute: " + bearer
	case !strings.EqualFold(in.Provider, "aws"):
		return "provider " + in.Provider
	case in.Label != Account:
		return "account " + in.Label
	case in.Host != Endpoint && !(in.Host == Monitoring && form.Get("Action") == "GetMetricData"):
		return "endpoint " + in.Host
	case in.Method != http.MethodPost || in.Path != "/":
		return "not a Query API call: " + in.Method + " " + in.Path
	case !strings.HasPrefix(in.Type, "application/x-www-form-urlencoded") || (len(in.Body) > 0 && string(in.Body) != "null"):
		return "not a form: " + in.Type
	}
	for k := range form {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") || strings.EqualFold(k, "Signature") ||
			strings.EqualFold(k, "AWSAccessKeyId") {
			return "the request was signed by the caller: " + k
		}
	}
	if u, err := url.Parse(in.Path); err != nil || u.RawQuery != "" {
		return "a query was sent: " + in.Path
	}
	return ""
}

// answer is the account's answer to one EC2 call.
func (f *Fake) answer(w http.ResponseWriter, call Call) {
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
	case "GetMetricData":
		f.metricData(w, call.Form)
	default:
		ec2Error(w, http.StatusBadRequest, "InvalidAction", call.Action)
	}
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

// metricData is CloudWatch's GetMetricData for NetworkOut: the hourly Sum of
// each instance asked about, over [StartTime, EndTime), oldest first, at most
// MaxDatapoints a page — which the caller must set — with the rest behind a
// NextToken, as CloudWatch pages: a query with more to come is PartialData.
func (f *Fake) metricData(w http.ResponseWriter, form url.Values) {
	from, err1 := time.Parse(time.RFC3339, form.Get("StartTime"))
	to, err2 := time.Parse(time.RFC3339, form.Get("EndTime"))
	if err1 != nil || err2 != nil {
		ec2Error(w, http.StatusBadRequest, "InvalidParameterValue", "StartTime and EndTime")
		return
	}
	most, err := strconv.Atoi(form.Get("MaxDatapoints"))
	if err != nil || most <= 0 {
		ec2Error(w, http.StatusBadRequest, "InvalidParameterValue", "MaxDatapoints must bound the page")
		return
	}
	skip := 0
	if token := form.Get("NextToken"); token != "" {
		if skip, err = strconv.Atoi(token); err != nil {
			ec2Error(w, http.StatusBadRequest, "InvalidNextToken", token)
			return
		}
	}
	type point struct {
		at    time.Time
		bytes int64
	}
	var b strings.Builder
	seen, next := 0, ""
	for i := 1; ; i++ {
		p := "MetricDataQueries.member." + strconv.Itoa(i) + "."
		id, ok := form[p+"Id"]
		if !ok {
			break
		}
		if form.Get(p+"MetricStat.Metric.MetricName") != "NetworkOut" || form.Get(p+"MetricStat.Stat") != "Sum" ||
			form.Get(p+"MetricStat.Period") != "3600" || form.Get(p+"MetricStat.Metric.Dimensions.member.1.Name") != "InstanceId" {
			ec2Error(w, http.StatusBadRequest, "InvalidParameterCombination", "not an hourly NetworkOut Sum")
			return
		}
		instance := form.Get(p + "MetricStat.Metric.Dimensions.member.1.Value")
		if status := f.uncounted[instance]; status != "" {
			fmt.Fprintf(&b, "<member><Id>%s</Id><Label>NetworkOut</Label><Timestamps/><Values/><StatusCode>%s</StatusCode></member>", xmlText(id[0]), status)
			continue
		}
		var points []point
		for h := from.UTC().Truncate(time.Hour); h.Before(to); h = h.Add(time.Hour) {
			if bytes, ok := f.sent[instance][h.Format("2006010215")]; ok {
				points = append(points, point{h, bytes})
			}
		}
		var stamps, values strings.Builder
		status := "Complete"
		for _, pt := range points {
			seen++
			if seen <= skip {
				continue
			}
			if seen > skip+most {
				status, next = "PartialData", strconv.Itoa(skip+most)
				break
			}
			fmt.Fprintf(&stamps, "<member>%s</member>", pt.at.Format(time.RFC3339))
			fmt.Fprintf(&values, "<member>%d.0</member>", pt.bytes)
		}
		fmt.Fprintf(&b, "<member><Id>%s</Id><Label>NetworkOut</Label><Timestamps>%s</Timestamps><Values>%s</Values><StatusCode>%s</StatusCode></member>",
			xmlText(id[0]), stamps.String(), values.String(), status)
	}
	token := ""
	if next != "" {
		token = "<NextToken>" + next + "</NextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetMetricDataResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/"><GetMetricDataResult><MetricDataResults>%s</MetricDataResults>%s<Messages/></GetMetricDataResult><ResponseMetadata><RequestId>cw-1</RequestId></ResponseMetadata></GetMetricDataResponse>`, b.String(), token)
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

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
