package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
)

// Copying what the administrator built into the organization Keycloak's users
// are in.
//
// Everything made before `keycloak install` belongs to the Local organization,
// and people who sign in through Keycloak are in another one. The server has no
// way to move a Space between organizations, so this copies: it reads the Local
// organization as the administrator, with the key the instance trusts, and
// writes into the Keycloak organization as someone who signed in there.
//
// A separate command, not a step of `keycloak install`, because the second
// half needs a person who does not exist until the install has finished: a
// Keycloak user, made a member of the organization, who has logged in with cub.
// And because it is a decision of its own. Some instances want a clean
// organization for their users and the administrator's experiments left behind.
//
// It copies the configuration, not its history. A Unit arrives with the data
// its head revision holds; its earlier revisions, and anything about where it
// was applied, stay in Local. Targets and workers are not copied at all: they
// stand for a cluster's credentials in the organization that registered them,
// and the copy would point at a worker that cannot connect to it.
//
// Re-running it is safe. A Space or Unit whose slug is already there is left as
// it is rather than overwritten, so a Unit someone has since changed in the
// Keycloak organization keeps their change.

// CopyOptions is what `cub server keycloak copy-local` needs.
type CopyOptions struct {
	// Instance locates the instance, the same way `keycloak install` does: its
	// out-dir holds the administrator key's public half and the API's port.
	Instance Options

	// ToContext is the cub context to write as, one logged in to the Keycloak
	// organization. Empty picks the only such context on this server.
	ToContext string

	// Spaces limits the copy to these slugs. Empty copies every Space.
	Spaces []string

	DryRun bool
}

// copySource is the external source each copied Unit's data is recorded as
// coming from, which the server shows as its change description.
//
// As a source rather than a description because the data endpoint does not
// keep a description; it records a merge from an external source, by name.
const copySource = "the Local organization (cub server keycloak copy-local)"

// RunCopyLocal copies the Local organization's Spaces, Units and Links into the
// Keycloak organization.
func RunCopyLocal(ctx context.Context, u UI, c *CopyOptions) error {
	o := &c.Instance
	if err := o.Defaults(); err != nil {
		return err
	}
	if err := requireInstalled(o); err != nil {
		return err
	}
	if err := requireKeycloakInstalled(o); err != nil {
		return err
	}
	if err := o.adoptPortsFromPreviousRender(); err != nil {
		return err
	}
	server := o.APIURL()

	u.step("Signing in to both organizations")
	from, adminKey, err := localAdminClient(o, server)
	if err != nil {
		return err
	}
	fromMe, err := from.VerifyAuth(ctx)
	if err != nil {
		return fmt.Errorf("signing in with the administrator key %s: %w", adminKey, err)
	}
	u.detail("from  %s, as the administrator (key %s)", orgLabel(ctx, from, fromMe), adminKey)

	store, err := CubStore()
	if err != nil {
		return err
	}
	toContext, err := pickDestinationContext(store, server, adminKey, c.ToContext)
	if err != nil {
		return err
	}
	to, err := contextClient(store, toContext)
	if err != nil {
		return err
	}
	toMe, err := to.VerifyAuth(ctx)
	if err != nil {
		return fmt.Errorf("signing in with cub context %q: %w\n\n"+
			"    Its session may have expired. Log in again, as a member of the Keycloak organization:\n"+
			"      cub --context %s auth login", toContext, err, toContext)
	}
	if toMe.OrganizationID == fromMe.OrganizationID {
		return fmt.Errorf("cub context %q is in the Local organization too, so there is nothing to copy into.\n\n"+
			"    Log in as a Keycloak user who is a member of the organization to copy into:\n"+
			"%s", toContext, keycloakLoginCommands(server))
	}
	u.detail("to    %s, as %s (context %s)", orgLabel(ctx, to, toMe), memberLabel(toMe), toContext)

	u.step("Reading what is there")
	src, err := readOrg(ctx, from, c.Spaces)
	if err != nil {
		return fmt.Errorf("reading the Local organization: %w", err)
	}
	if missing := missingSpaces(src, c.Spaces); len(missing) > 0 {
		return fmt.Errorf("the Local organization has no Space %s", strings.Join(missing, ", "))
	}
	dst, err := readOrg(ctx, to, nil)
	if err != nil {
		return fmt.Errorf("reading the Keycloak organization: %w", err)
	}
	plan := planCopy(src, dst)
	left, err := leftBehind(ctx, from, src)
	if err != nil {
		return fmt.Errorf("reading the Local organization: %w", err)
	}

	if c.DryRun {
		reportPlan(u, plan, left, true)
		return nil
	}

	u.step("Copying")
	if err := plan.apply(ctx, u, from, to); err != nil {
		return err
	}
	reportPlan(u, plan, left, false)
	return nil
}

// localAdminClient signs in as the administrator the instance trusts, with the
// private half cub keeps in its key store.
//
// By the key rather than through a cub context: the key is what the instance
// records, so it finds the right identity whichever context is current, and it
// mints a fresh session rather than depending on one that may have expired.
func localAdminClient(o *Options, server string) (*cubapi.Client, string, error) {
	_, adminJWK, _, err := readPrior(o.OutDir)
	if err != nil {
		return nil, "", err
	}
	name := heldAdminKeyName(adminJWK)
	if name == "" {
		return nil, "", fmt.Errorf("the administrator key this instance trusts is not in cub's key store.\n" +
			"    Copying reads the Local organization as the administrator, so it needs that key")
	}
	store, err := CubStore()
	if err != nil {
		return nil, "", err
	}
	privateJWK, err := os.ReadFile(store.KeyPath(name))
	if err != nil {
		return nil, "", fmt.Errorf("reading the administrator key: %w", err)
	}
	signer, err := cubapi.NewAssertionSigner(privateJWK, "", "")
	if err != nil {
		return nil, "", fmt.Errorf("the administrator key %s: %w", name, err)
	}
	client, err := cubapi.NewClient(cubapi.ClientOptions{
		ServerURL:     server,
		Authenticator: cubapi.AssertionAuthenticator(server, signer),
		UserAgent:     "cub-server",
	})
	return client, name, err
}

// pickDestinationContext chooses the cub context to write as.
//
// Named, it is used as it is, and checked later against the organization it
// signs in to. Unnamed, it is the one context on this server that is not the
// administrator's: the administrator's reaches Local only, so any other one on
// the same server is someone who logged in through Keycloak. More than one is
// a choice this cannot make for the operator.
func pickDestinationContext(store *cubapi.Store, server, adminKey, named string) (string, error) {
	if named != "" {
		if _, err := store.Context(named); err != nil {
			return "", fmt.Errorf("no cub context %q: %w", named, err)
		}
		return named, nil
	}
	candidates := destinationCandidates(store.Contexts(), server, adminKey)
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf("no cub context is logged in to %s as anyone but the administrator.\n\n"+
			"    Log in as a Keycloak user who is a member of the organization to copy into,\n"+
			"    then run this again:\n"+
			"%s", server, keycloakLoginCommands(server))
	default:
		return "", fmt.Errorf("more than one cub context is logged in to %s: %s.\n"+
			"    Name the one in the organization to copy into with --to-context",
			server, strings.Join(candidates, ", "))
	}
}

// keycloakLoginCommands is how to log in through Keycloak on a machine where
// the administrator is logged in.
//
// A context of its own first, because `cub auth login` signs in again with the
// key the current context last used, --new-context or not. Right after an
// install the current context is the administrator's, so a plain login there
// makes a second administrator context rather than reaching Keycloak.
func keycloakLoginCommands(server string) string {
	return "      cub context create keycloak --server=" + server + " --use\n" +
		"      cub auth login"
}

// destinationCandidates names the contexts on server that do not authenticate
// with the administrator key.
func destinationCandidates(contexts []*cubapi.Context, server, adminKey string) []string {
	var names []string
	for _, c := range contexts {
		if !cubapi.SameServer(c.Coordinate.ServerURL, server) {
			continue
		}
		if key := c.Metadata.PrivateKey; key != "" &&
			(key == adminKey || strings.TrimSuffix(filepath.Base(key), ".jwk") == adminKey) {
			continue
		}
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// contextClient builds a client from one named cub context's session.
func contextClient(store *cubapi.Store, name string) (*cubapi.Client, error) {
	c, err := store.Context(name)
	if err != nil {
		return nil, err
	}
	token, err := store.TokenData(c)
	if err != nil {
		return nil, fmt.Errorf("cub context %q has no session: %w", name, err)
	}
	return cubapi.NewClient(cubapi.ClientOptions{
		ServerURL: c.Coordinate.ServerURL,
		Token:     token.AccessToken,
		UserAgent: "cub-server",
	})
}

// orgLabel names an organization for the output, falling back to its ID.
func orgLabel(ctx context.Context, c *cubapi.Client, me *goclientnew.OrganizationMember) string {
	res, err := c.API.GetOrganizationWithResponse(ctx, me.OrganizationID, &goclientnew.GetOrganizationParams{})
	if err == nil && res.JSON200 != nil && res.JSON200.Organization != nil {
		org := res.JSON200.Organization
		if org.DisplayName != "" {
			return org.DisplayName
		}
		return org.Slug
	}
	return me.OrganizationID.String()
}

func memberLabel(me *goclientnew.OrganizationMember) string {
	for _, s := range []string{me.Username, me.DisplayName, me.Slug} {
		if s != "" {
			return s
		}
	}
	return me.UserID.String()
}

// orgContents is what one organization holds, as far as a copy is concerned.
type orgContents struct {
	Spaces []*goclientnew.Space
	Units  []*goclientnew.Unit
	Links  []*goclientnew.Link
}

// readOrg lists an organization's Spaces, and the Units and Links in them,
// limited to the named slugs when there are any.
func readOrg(ctx context.Context, c *cubapi.Client, slugs []string) (*orgContents, error) {
	spaces, err := cubapi.ListSpaces(ctx, c, cubapi.Where{}.InStrings("Slug", slugs), cubapi.ListOpts{OrderBy: "Slug"})
	if err != nil {
		return nil, err
	}
	out := &orgContents{}
	var ids []goclientnew.UUID
	for _, s := range spaces {
		if s.Space != nil {
			out.Spaces = append(out.Spaces, s.Space)
			ids = append(ids, s.Space.SpaceID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	units, err := cubapi.ListUnits(ctx, c, cubapi.Where{}.In("SpaceID", ids), cubapi.ListOpts{OrderBy: "Slug"})
	if err != nil {
		return nil, err
	}
	for _, x := range units {
		if x.Unit != nil {
			out.Units = append(out.Units, x.Unit)
		}
	}
	links, err := cubapi.ListLinks(ctx, c, cubapi.Where{}.In("SpaceID", ids), cubapi.ListOpts{OrderBy: "Slug"})
	if err != nil {
		return nil, err
	}
	for _, x := range links {
		if x.Link != nil {
			out.Links = append(out.Links, x.Link)
		}
	}
	return out, nil
}

// missingSpaces names the requested slugs the organization has no Space for.
func missingSpaces(src *orgContents, slugs []string) []string {
	have := map[string]bool{}
	for _, s := range src.Spaces {
		have[s.Slug] = true
	}
	var missing []string
	for _, slug := range slugs {
		if !have[slug] {
			missing = append(missing, slug)
		}
	}
	return missing
}

// copyPlan is what a copy will do, worked out before anything is written so a
// dry run and a real one report the same thing.
type copyPlan struct {
	Spaces []*spaceCopy
	Links  []*linkCopy

	// LinksOut are the Links that point outside the Spaces being copied, which
	// cannot be made without the Unit at their other end.
	LinksOut []*goclientnew.Link
}

type spaceCopy struct {
	Src *goclientnew.Space
	// Dst is the destination's Space of this slug: the one already there, or,
	// once applied, the one the copy created.
	Dst *goclientnew.Space
	// Create is set when the destination has no Space of this slug.
	Create bool
	Units  []*unitCopy
}

type unitCopy struct {
	Src *goclientnew.Unit
	// Dst is the destination's Unit of this slug in the Space: the one already
	// there, or, once applied, the copy.
	Dst *goclientnew.Unit
	// Create is set when the destination Space has no Unit of this slug.
	Create bool
}

type linkCopy struct {
	Src *goclientnew.Link
	// Create is set when the destination Space has no Link of this slug.
	Create bool
}

// planCopy matches the source against the destination by slug.
//
// Slugs, because IDs are the one thing that cannot carry over: every entity in
// the destination is new, with an ID of its own. A slug is unique within its
// Space, and a Space's slug within its organization, so the pair names the same
// thing on both sides.
func planCopy(src, dst *orgContents) *copyPlan {
	dstSpaces := map[string]*goclientnew.Space{}
	for _, s := range dst.Spaces {
		dstSpaces[s.Slug] = s
	}
	dstSpaceSlug := map[goclientnew.UUID]string{}
	for _, s := range dst.Spaces {
		dstSpaceSlug[s.SpaceID] = s.Slug
	}
	dstUnits := map[[2]string]*goclientnew.Unit{}
	for _, x := range dst.Units {
		dstUnits[[2]string{dstSpaceSlug[x.SpaceID], x.Slug}] = x
	}
	dstLinks := map[[2]string]bool{}
	for _, l := range dst.Links {
		dstLinks[[2]string{dstSpaceSlug[l.SpaceID], l.Slug}] = true
	}

	plan := &copyPlan{}
	bySpaceID := map[goclientnew.UUID]*spaceCopy{}
	for _, s := range src.Spaces {
		sc := &spaceCopy{Src: s, Dst: dstSpaces[s.Slug]}
		sc.Create = sc.Dst == nil
		plan.Spaces = append(plan.Spaces, sc)
		bySpaceID[s.SpaceID] = sc
	}
	copiedUnit := map[goclientnew.UUID]bool{}
	for _, x := range src.Units {
		sc := bySpaceID[x.SpaceID]
		if sc == nil {
			continue
		}
		uc := &unitCopy{Src: x}
		if !sc.Create {
			uc.Dst = dstUnits[[2]string{sc.Src.Slug, x.Slug}]
		}
		uc.Create = uc.Dst == nil
		sc.Units = append(sc.Units, uc)
		copiedUnit[x.UnitID] = true
	}
	for _, l := range src.Links {
		sc := bySpaceID[l.SpaceID]
		if sc == nil {
			continue
		}
		if !copiedUnit[l.FromUnitID] || !copiedUnit[l.ToUnitID] {
			plan.LinksOut = append(plan.LinksOut, l)
			continue
		}
		plan.Links = append(plan.Links, &linkCopy{Src: l, Create: sc.Create || !dstLinks[[2]string{sc.Src.Slug, l.Slug}]})
	}
	return plan
}

// apply carries out the plan, Spaces first, then their Units, then the Links
// between them, which need both of their Units to exist.
func (p *copyPlan) apply(ctx context.Context, u UI, from, to *cubapi.Client) error {
	spaceID := map[goclientnew.UUID]goclientnew.UUID{} // source Space ID -> destination
	unitID := map[goclientnew.UUID]goclientnew.UUID{}  // source Unit ID -> destination

	for _, sc := range p.Spaces {
		if sc.Create {
			created, err := createSpace(ctx, to, sc.Src)
			if err != nil {
				return fmt.Errorf("creating Space %s: %w", sc.Src.Slug, err)
			}
			sc.Dst = created
			u.detail("space %s: created", sc.Src.Slug)
		} else {
			u.detail("space %s: already there", sc.Src.Slug)
		}
		spaceID[sc.Src.SpaceID] = sc.Dst.SpaceID

		for _, uc := range sc.Units {
			if !uc.Create {
				unitID[uc.Src.UnitID] = uc.Dst.UnitID
				continue
			}
			created, err := copyUnit(ctx, from, to, uc.Src, sc.Dst.SpaceID)
			if err != nil {
				return fmt.Errorf("copying Unit %s/%s: %w", sc.Src.Slug, uc.Src.Slug, err)
			}
			uc.Dst = created
			unitID[uc.Src.UnitID] = created.UnitID
			u.detail("  unit %s/%s: copied", sc.Src.Slug, uc.Src.Slug)
		}
	}

	for _, lc := range p.Links {
		if !lc.Create {
			continue
		}
		if err := createLink(ctx, to, lc.Src, spaceID, unitID); err != nil {
			return fmt.Errorf("creating Link %s: %w", lc.Src.Slug, err)
		}
		u.detail("  link %s: created", lc.Src.Slug)
	}
	return nil
}

// createSpace makes a Space carrying the source's names, labels and
// annotations, and nothing that refers to another entity in the source.
func createSpace(ctx context.Context, c *cubapi.Client, src *goclientnew.Space) (*goclientnew.Space, error) {
	body := goclientnew.Space{
		Slug:        src.Slug,
		DisplayName: src.DisplayName,
		Labels:      src.Labels,
		Annotations: src.Annotations,
	}
	res, err := c.API.CreateSpaceWithResponse(ctx, &goclientnew.CreateSpaceParams{}, body)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	return res.JSON200, nil
}

// copyUnit makes a Unit in spaceID with the source's names, labels,
// annotations and toolchain, then gives it the source's head data.
//
// Without its target: targets are not copied, and one in the destination of
// the same slug is a different cluster's until someone says otherwise.
func copyUnit(ctx context.Context, from, to *cubapi.Client, src *goclientnew.Unit, spaceID goclientnew.UUID) (*goclientnew.Unit, error) {
	data, err := cubapi.UnitData(ctx, from, src.SpaceID, src.UnitID)
	if err != nil {
		return nil, fmt.Errorf("reading its data: %w", err)
	}
	body := goclientnew.Unit{
		Slug:          src.Slug,
		DisplayName:   src.DisplayName,
		Labels:        src.Labels,
		Annotations:   src.Annotations,
		ToolchainType: src.ToolchainType,
	}
	res, err := to.API.CreateUnitWithResponse(ctx, spaceID, &goclientnew.CreateUnitParams{}, body)
	if cubapi.IsAPIError(err, res) {
		return nil, cubapi.InterpretErrorGeneric(err, res)
	}
	if res.JSON200.Error != nil {
		return nil, fmt.Errorf("%s", res.JSON200.Error.Message)
	}
	created := res.JSON200.Unit
	if data == "" {
		return created, nil
	}

	source := copySource
	up, err := to.API.UploadUnitDataWithBodyWithResponse(ctx, spaceID, created.UnitID,
		&goclientnew.UploadUnitDataParams{MergeExternalSource: &source},
		"application/octet-stream", strings.NewReader(data))
	if cubapi.IsAPIError(err, up) {
		return nil, fmt.Errorf("writing its data: %w", cubapi.InterpretErrorGeneric(err, up))
	}
	if up.JSON200.Error != nil {
		return nil, fmt.Errorf("writing its data: %s", up.JSON200.Error.Message)
	}
	if up.JSON200.Unit != nil {
		created = up.JSON200.Unit
	}
	return created, nil
}

// createLink makes the source Link between the copies of its two Units.
func createLink(ctx context.Context, c *cubapi.Client, src *goclientnew.Link,
	spaceID, unitID map[goclientnew.UUID]goclientnew.UUID) error {
	body := goclientnew.Link{
		Slug:                   src.Slug,
		DisplayName:            src.DisplayName,
		Labels:                 src.Labels,
		Annotations:            src.Annotations,
		FromUnitID:             unitID[src.FromUnitID],
		ToUnitID:               unitID[src.ToUnitID],
		ToSpaceID:              spaceID[src.ToSpaceID],
		UpdateType:             src.UpdateType,
		AutoUpdate:             src.AutoUpdate,
		MergeEnableSubtraction: src.MergeEnableSubtraction,
		UpstreamPaths:          src.UpstreamPaths,
		DownstreamPaths:        src.DownstreamPaths,
	}
	res, err := c.API.CreateLinkWithResponse(ctx, spaceID[src.SpaceID], &goclientnew.CreateLinkParams{}, body)
	if cubapi.IsAPIError(err, res) {
		return cubapi.InterpretErrorGeneric(err, res)
	}
	return nil
}

// leftOut counts what a copy does not carry, so the report can say so rather
// than leaving the operator to find it missing.
type leftOut struct {
	Targets, Triggers, Filters, Views int
	// TargetedUnits are the Units that had a target, which their copies do not.
	TargetedUnits int
	// Clones are the Units copied from an upstream Unit, whose copies are plain
	// Units: the upgrade Link between them is copied, the clone relation is not.
	Clones int
}

func leftBehind(ctx context.Context, c *cubapi.Client, src *orgContents) (leftOut, error) {
	var l leftOut
	for _, x := range src.Units {
		if x.TargetID != nil {
			l.TargetedUnits++
		}
		if x.UpstreamUnitID != nil {
			l.Clones++
		}
	}
	var ids []goclientnew.UUID
	for _, s := range src.Spaces {
		ids = append(ids, s.SpaceID)
	}
	if len(ids) == 0 {
		return l, nil
	}
	in := cubapi.Where{}.In("SpaceID", ids)
	targets, err := cubapi.ListTargets(ctx, c, in, cubapi.ListOpts{Select: "TargetID"})
	if err != nil {
		return l, err
	}
	triggers, err := cubapi.ListTriggers(ctx, c, in, cubapi.ListOpts{Select: "TriggerID"})
	if err != nil {
		return l, err
	}
	filters, err := cubapi.ListFilters(ctx, c, in, cubapi.ListOpts{Select: "FilterID"})
	if err != nil {
		return l, err
	}
	views, err := cubapi.ListViews(ctx, c, in, cubapi.ListOpts{Select: "ViewID"})
	if err != nil {
		return l, err
	}
	l.Targets, l.Triggers, l.Filters, l.Views = len(targets), len(triggers), len(filters), len(views)
	return l, nil
}

// reportPlan says what was copied, or would be, and what was not.
func reportPlan(u UI, p *copyPlan, left leftOut, dryRun bool) {
	var spacesNew, spacesThere, unitsNew, unitsThere, linksNew, linksThere int
	for _, sc := range p.Spaces {
		if sc.Create {
			spacesNew++
		} else {
			spacesThere++
		}
		for _, uc := range sc.Units {
			if uc.Create {
				unitsNew++
			} else {
				unitsThere++
			}
		}
	}
	for _, lc := range p.Links {
		if lc.Create {
			linksNew++
		} else {
			linksThere++
		}
	}

	title, verb := "Copied into the Keycloak organization.", "copied"
	if dryRun {
		title, verb = "Dry run: nothing was written. A copy would make:", "to copy"
		var lines []string
		for _, sc := range p.Spaces {
			state := "already there; its new Units are added to it"
			if sc.Create {
				state = "new"
			}
			lines = append(lines, fmt.Sprintf("space %s (%s)", sc.Src.Slug, state))
			for _, uc := range sc.Units {
				if uc.Create {
					lines = append(lines, "  unit "+uc.Src.Slug)
				}
			}
			for _, lc := range p.Links {
				if lc.Create && lc.Src.SpaceID == sc.Src.SpaceID {
					lines = append(lines, "  link "+lc.Src.Slug)
				}
			}
		}
		u.section(title, lines...)
		title = "In all:"
	}

	lines := []string{
		fmt.Sprintf("spaces   %d %s, %d already there", spacesNew, verb, spacesThere),
		fmt.Sprintf("units    %d %s, %d already there and left as they are", unitsNew, verb, unitsThere),
		fmt.Sprintf("links    %d %s, %d already there", linksNew, verb, linksThere),
	}
	var notes []string
	if n := len(p.LinksOut); n > 0 {
		notes = append(notes, fmt.Sprintf("%d link(s) to Units outside the Spaces copied", n))
	}
	if left.Targets > 0 || left.TargetedUnits > 0 {
		notes = append(notes, fmt.Sprintf("%d target(s), and the target of %d unit(s): a target is a cluster's"+
			" credentials in the organization that registered it, so set up workers and targets again there", left.Targets, left.TargetedUnits))
	}
	if left.Triggers > 0 {
		notes = append(notes, fmt.Sprintf("%d trigger(s)", left.Triggers))
	}
	if left.Filters > 0 {
		notes = append(notes, fmt.Sprintf("%d filter(s)", left.Filters))
	}
	if left.Views > 0 {
		notes = append(notes, fmt.Sprintf("%d view(s)", left.Views))
	}
	if left.Clones > 0 {
		notes = append(notes, fmt.Sprintf("the clone relation of %d unit(s), which arrive as plain units", left.Clones))
	}
	if len(notes) > 0 {
		lines = append(lines, "", "Not copied:")
		for _, n := range notes {
			lines = append(lines, "  "+n)
		}
	}
	lines = append(lines, "",
		"Units arrive with their current data, not their revision history, and nothing",
		"is deleted from the Local organization.")
	if dryRun {
		lines = append(lines, "", "Re-run without --dry-run to copy.")
	}
	u.section(title, lines...)
}
