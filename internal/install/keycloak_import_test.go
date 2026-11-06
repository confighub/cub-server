package install

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/google/uuid"
)

func space(slug string) *goclientnew.Space {
	return &goclientnew.Space{Slug: slug, SpaceID: uuid.New()}
}

func unitIn(s *goclientnew.Space, slug string) *goclientnew.Unit {
	return &goclientnew.Unit{Slug: slug, SpaceID: s.SpaceID, UnitID: uuid.New(), ToolchainType: "Kubernetes/YAML"}
}

func link(s *goclientnew.Space, slug string, from, to *goclientnew.Unit) *goclientnew.Link {
	return &goclientnew.Link{Slug: slug, SpaceID: s.SpaceID, LinkID: uuid.New(),
		FromUnitID: from.UnitID, ToUnitID: to.UnitID, ToSpaceID: to.SpaceID}
}

func TestPlanCopyMatchesBySlug(t *testing.T) {
	// Local: default {a, b}, apps {web, db}, a link web->db and one web->a.
	lDefault, lApps := space("default"), space("apps")
	a, b := unitIn(lDefault, "a"), unitIn(lDefault, "b")
	web, db := unitIn(lApps, "web"), unitIn(lApps, "db")
	src := &orgContents{
		Spaces: []*goclientnew.Space{lApps, lDefault},
		Units:  []*goclientnew.Unit{a, b, web, db},
		Links:  []*goclientnew.Link{link(lApps, "web-db", web, db), link(lApps, "web-a", web, a)},
	}

	// Keycloak org: its own default, already holding a Unit called a.
	kDefault := space("default")
	kA := unitIn(kDefault, "a")
	dst := &orgContents{Spaces: []*goclientnew.Space{kDefault}, Units: []*goclientnew.Unit{kA}}

	plan := planCopy(src, dst)

	got := map[string]bool{}
	for _, sc := range plan.Spaces {
		got["space "+sc.Src.Slug] = sc.Create
		for _, uc := range sc.Units {
			got["unit "+sc.Src.Slug+"/"+uc.Src.Slug] = uc.Create
		}
	}
	for _, lc := range plan.Links {
		got["link "+lc.Src.Slug] = lc.Create
	}
	want := map[string]bool{
		"space apps":     true,
		"space default":  false,
		"unit apps/web":  true,
		"unit apps/db":   true,
		"unit default/a": false, // already there: left as it is
		"unit default/b": true,
		"link web-db":    true,
		"link web-a":     true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan:\n got %v\nwant %v", got, want)
	}

	for _, sc := range plan.Spaces {
		for _, uc := range sc.Units {
			if uc.Src == a && uc.Dst != kA {
				t.Errorf("default/a should map onto the destination's existing Unit")
			}
		}
	}
	if len(plan.SkippedLinks) != 0 {
		t.Errorf("SkippedLinks = %d, want 0", len(plan.SkippedLinks))
	}
}

func TestPlanCopyLeavesOutLinksToSpacesNotCopied(t *testing.T) {
	// --space apps: the link from web to default/a has nothing at its far end.
	lDefault, lApps := space("default"), space("apps")
	a, web := unitIn(lDefault, "a"), unitIn(lApps, "web")
	src := &orgContents{
		Spaces: []*goclientnew.Space{lApps},
		Units:  []*goclientnew.Unit{web},
		Links:  []*goclientnew.Link{link(lApps, "web-a", web, a)},
	}
	plan := planCopy(src, &orgContents{})
	if len(plan.Links) != 0 || len(plan.SkippedLinks) != 1 || plan.SkippedLinks[0].Reason != skipOutside {
		t.Fatalf("Links = %d, SkippedLinks = %+v; want none copied and one skipped as outside", len(plan.Links), plan.SkippedLinks)
	}
}

func TestPlanCopySkipsExistingLink(t *testing.T) {
	lApps := space("apps")
	web, db := unitIn(lApps, "web"), unitIn(lApps, "db")
	kApps := space("apps")
	kWeb, kDB := unitIn(kApps, "web"), unitIn(kApps, "db")
	src := &orgContents{Spaces: []*goclientnew.Space{lApps}, Units: []*goclientnew.Unit{web, db},
		Links: []*goclientnew.Link{link(lApps, "web-db", web, db)}}
	dst := &orgContents{Spaces: []*goclientnew.Space{kApps}, Units: []*goclientnew.Unit{kWeb, kDB},
		Links: []*goclientnew.Link{link(kApps, "web-db", kWeb, kDB)}}

	plan := planCopy(src, dst)
	if len(plan.Links) != 1 || plan.Links[0].Create {
		t.Fatalf("an existing link should be planned and not created: %+v", plan.Links)
	}
}

func TestDestinationCandidates(t *testing.T) {
	ctx := func(name, server, key string) *cubapi.Context {
		return &cubapi.Context{Name: name,
			Coordinate: cubapi.Coordinate{ServerURL: server},
			Metadata:   cubapi.Metadata{PrivateKey: key}}
	}
	contexts := []*cubapi.Context{
		ctx("admin", "http://localhost:30080", "confighub-admin"),
		ctx("admin-by-path", "http://localhost:30080/", "/home/x/.confighub/keys/confighub-admin.jwk"),
		ctx("other-server", "http://localhost:31080", ""),
		ctx("bob", "http://localhost:30080", ""),
		ctx("alice", "http://localhost:30080", ""),
		ctx("ci", "http://localhost:30080", "ci-bot"),
	}
	got := destinationCandidates(contexts, "http://localhost:30080", "confighub-admin")
	want := []string{"alice", "bob", "ci"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReportPlanDryRunSaysNothingWasWritten(t *testing.T) {
	lApps := space("apps")
	web := unitIn(lApps, "web")
	web.TargetID = &lApps.SpaceID // any non-nil target
	plan := planCopy(&orgContents{Spaces: []*goclientnew.Space{lApps}, Units: []*goclientnew.Unit{web}}, &orgContents{})

	var out bytes.Buffer
	reportPlan(UI{Out: &out}, plan, leftOut{Targets: 1, TargetedUnits: 1}, true)
	s := out.String()
	for _, want := range []string{"Dry run", "space apps (new)", "unit web", "1 target(s)", "--dry-run"} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q:\n%s", want, s)
		}
	}
}

func TestPlanCopySkipsLinksThatRunAnInvocation(t *testing.T) {
	lApps := space("apps")
	web, db := unitIn(lApps, "web"), unitIn(lApps, "db")
	l := link(lApps, "upsert", web, db)
	l.UpdateType = "Upsert"
	inv := uuid.New()
	l.TransformInvocationID = &inv
	plan := planCopy(&orgContents{Spaces: []*goclientnew.Space{lApps}, Units: []*goclientnew.Unit{web, db},
		Links: []*goclientnew.Link{l}}, &orgContents{})
	if len(plan.Links) != 0 || len(plan.SkippedLinks) != 1 || plan.SkippedLinks[0].Reason != skipInvocation {
		t.Fatalf("Links = %d, SkippedLinks = %+v; want the Invocation's link skipped", len(plan.Links), plan.SkippedLinks)
	}
}

func TestPlanCopyMarksMergeLinksBehindTheirUpstream(t *testing.T) {
	lApps := space("apps")
	base, prod := unitIn(lApps, "base"), unitIn(lApps, "prod")
	base.HeadRevisionNum = 7
	current, behind := link(lApps, "current", prod, base), link(lApps, "behind", prod, base)
	current.UpdateType, current.UpstreamLastMergedRevisionNum = "MergeUnits", 7
	behind.UpdateType, behind.UpstreamLastMergedRevisionNum = "MergeUnits", 5
	plan := planCopy(&orgContents{Spaces: []*goclientnew.Space{lApps}, Units: []*goclientnew.Unit{base, prod},
		Links: []*goclientnew.Link{current, behind}}, &orgContents{})
	got := map[string]bool{}
	for _, lc := range plan.Links {
		got[lc.Src.Slug] = lc.Behind
	}
	if want := map[string]bool{"current": false, "behind": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Behind = %v, want %v", got, want)
	}
}

func TestLinkBodyKeepsSettingsAndRemapsReferences(t *testing.T) {
	lApps := space("apps")
	base, prod := unitIn(lApps, "base"), unitIn(lApps, "prod")
	backing, upstreamLink := uuid.New(), uuid.New()
	src := link(lApps, "prod-from-base", prod, base)
	src.UpdateType = "MergeUnits"
	src.AutoUpdate = true
	src.WhereResource = "ResourceType = 'apps/v1/Deployment'"
	src.WhereMutation = "Path LIKE 'spec.replicas%'"
	src.ManualBindings = &goclientnew.BindingList{}
	src.Squash = true
	src.Labels = map[string]string{"tier": "web"}
	src.UpstreamLastMergedRevisionNum, src.DownstreamLastMergedRevisionNum = 12, 9
	src.Hash, src.Stale, src.Version = "abc", true, 4
	src.Bindings = &goclientnew.BindingList{}
	src.BackingUnitID, src.UpstreamLinkID = &backing, &upstreamLink

	dSpace, dBase, dProd := uuid.New(), uuid.New(), uuid.New()
	body := linkBody(src,
		map[goclientnew.UUID]goclientnew.UUID{lApps.SpaceID: dSpace},
		map[goclientnew.UUID]goclientnew.UUID{base.UnitID: dBase, prod.UnitID: dProd},
		map[goclientnew.UUID]int64{base.UnitID: 2, prod.UnitID: 3})

	// What the Link does, kept.
	if body.UpdateType != "MergeUnits" || !body.AutoUpdate || body.WhereResource != src.WhereResource ||
		body.WhereMutation != src.WhereMutation || body.ManualBindings == nil || !body.Squash ||
		body.Labels["tier"] != "web" || body.Slug != src.Slug {
		t.Errorf("settings not carried over: %+v", body)
	}
	// Between the copies.
	if body.FromUnitID != dProd || body.ToUnitID != dBase || body.ToSpaceID != dSpace {
		t.Errorf("units = %v -> %v in %v, want the copies", body.FromUnitID, body.ToUnitID, body.ToSpaceID)
	}
	// Merged up to what the copies hold, not the source's revision numbers.
	if body.UpstreamLastMergedRevisionNum != 2 || body.DownstreamLastMergedRevisionNum != 3 {
		t.Errorf("merge base = %d/%d, want the copies' heads 2/3",
			body.UpstreamLastMergedRevisionNum, body.DownstreamLastMergedRevisionNum)
	}
	// Nothing the server owns, and nothing naming the source organization.
	var zero goclientnew.UUID
	if body.LinkID != zero || body.SpaceID != zero || body.Hash != "" || body.Stale || body.Version != 0 ||
		body.Bindings != nil || body.BackingUnitID != nil || body.UpstreamLinkID != nil {
		t.Errorf("server-owned or source references kept: %+v", body)
	}
	// The source is left as it was.
	if src.FromUnitID != prod.UnitID || src.UpstreamLastMergedRevisionNum != 12 {
		t.Errorf("linkBody changed its source")
	}
}

func TestLinkBodyLeavesOtherLinksUnmerged(t *testing.T) {
	lApps := space("apps")
	web, ns := unitIn(lApps, "web"), unitIn(lApps, "ns")
	src := link(lApps, "web-to-ns", web, ns)
	src.UpdateType = "NeedsProvides"
	src.UpstreamLastMergedRevisionNum = 4
	body := linkBody(src, map[goclientnew.UUID]goclientnew.UUID{},
		map[goclientnew.UUID]goclientnew.UUID{web.UnitID: uuid.New(), ns.UnitID: uuid.New()},
		map[goclientnew.UUID]int64{web.UnitID: 2, ns.UnitID: 2})
	if body.UpstreamLastMergedRevisionNum != 0 || body.DownstreamLastMergedRevisionNum != 0 {
		t.Errorf("merge base = %d/%d on a NeedsProvides link, want 0/0",
			body.UpstreamLastMergedRevisionNum, body.DownstreamLastMergedRevisionNum)
	}
}

// fakeAPI answers the calls copyUnit makes, failing the data upload.
type fakeAPI struct {
	unitID  goclientnew.UUID
	deleted []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/data"):
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "apiVersion: v1\nkind: ConfigMap\n")
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/unit"):
		_ = json.NewEncoder(w).Encode(goclientnew.UnitCreateOrUpdateResponse{
			Unit: &goclientnew.Unit{UnitID: f.unitID, Slug: "web", HeadRevisionNum: 1}})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/data"):
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"Message":"disk full","Type":"Internal","Status":500}`)
	case r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

func TestCopyUnitRemovesTheUnitWhenItsDataCannotBeWritten(t *testing.T) {
	api := &fakeAPI{unitID: uuid.New()}
	srv := httptest.NewServer(api)
	defer srv.Close()
	client, err := cubapi.NewClient(cubapi.ClientOptions{ServerURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}

	lApps := space("apps")
	dst := uuid.New()
	_, err = copyUnit(context.Background(), client, client, unitIn(lApps, "web"), dst)
	if err == nil || !strings.Contains(err.Error(), "writing its data") {
		t.Fatalf("err = %v, want the failed write", err)
	}
	want := "/api/space/" + dst.String() + "/unit/" + api.unitID.String()
	if len(api.deleted) != 1 || api.deleted[0] != want {
		t.Fatalf("deleted %v, want %s: an empty Unit left behind reads as copied on the next run", api.deleted, want)
	}
}
