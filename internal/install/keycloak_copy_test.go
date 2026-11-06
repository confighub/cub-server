package install

import (
	"bytes"
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
	if len(plan.LinksOut) != 0 {
		t.Errorf("LinksOut = %d, want 0", len(plan.LinksOut))
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
	if len(plan.Links) != 0 || len(plan.LinksOut) != 1 {
		t.Fatalf("Links = %d, LinksOut = %d; want 0 and 1", len(plan.Links), len(plan.LinksOut))
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
