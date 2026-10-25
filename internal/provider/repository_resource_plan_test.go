package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// planRepository runs PlanResourceChange for ghost_repository from a fixed
// prior state ("alice/old", owner omitted from config) with the given config
// values, returning the paths that force replacement and the planned values.
func planRepository(t *testing.T, config map[string]tftypes.Value) ([]*tftypes.AttributePath, map[string]tftypes.Value) {
	t.Helper()
	ctx := context.Background()

	server, err := testAccProtoV6ProviderFactories["ghost"]()
	if err != nil {
		t.Fatal(err)
	}
	schemaResp, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	typ := schemaResp.ResourceSchemas["ghost_repository"].ValueType().(tftypes.Object)

	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	null := func(k string) tftypes.Value { return tftypes.NewValue(typ.AttributeTypes[k], nil) }

	prior := map[string]tftypes.Value{
		"id": str("alice/old"), "name": str("old"), "owner": str("alice"), "description": null("description"),
		"visibility": str("private"), "default_branch": str("main"), "auto_init": null("auto_init"),
		"gitignore_template": null("gitignore_template"), "license_template": null("license_template"),
		"slug": str("old"), "full_name": str("alice/old"), "repo_id": tftypes.NewValue(tftypes.Number, 1),
		"node_id": str("R1"), "html_url": str("h"), "clone_url": str("c"), "ssh_url": str(""),
		"private": tftypes.NewValue(tftypes.Bool, true), "fork": tftypes.NewValue(tftypes.Bool, false),
	}
	optionalOnly := map[string]bool{"description": true, "auto_init": true, "gitignore_template": true, "license_template": true}

	// Build config and the proposed new state the way Terraform core does:
	// configured values win, computed attributes fall back to prior state.
	cfg := map[string]tftypes.Value{"name": str("old")}
	for k, v := range config {
		cfg[k] = v
	}
	fullConfig := map[string]tftypes.Value{}
	proposed := map[string]tftypes.Value{}
	for k := range typ.AttributeTypes {
		fullConfig[k] = null(k)
		proposed[k] = prior[k]
		if v, ok := cfg[k]; ok {
			fullConfig[k] = v
			proposed[k] = v
		} else if optionalOnly[k] {
			proposed[k] = null(k)
		}
	}

	dv := func(m map[string]tftypes.Value) *tfprotov6.DynamicValue {
		d, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, m))
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	resp, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "ghost_repository",
		PriorState:       dv(prior),
		Config:           dv(fullConfig),
		ProposedNewState: dv(proposed),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("plan diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}

	planned, err := resp.PlannedState.Unmarshal(typ)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]tftypes.Value
	if err := planned.As(&out); err != nil {
		t.Fatal(err)
	}

	return resp.RequiresReplace, out
}

func TestRepositoryPlanRenameIsInPlace(t *testing.T) {
	replace, planned := planRepository(t, map[string]tftypes.Value{
		"name":        tftypes.NewValue(tftypes.String, "new"),
		"description": tftypes.NewValue(tftypes.String, "d"),
	})
	if len(replace) != 0 {
		t.Fatalf("rename with owner omitted must not force replacement, got RequiresReplace %v", replace)
	}

	if !planned["owner"].Equal(tftypes.NewValue(tftypes.String, "alice")) {
		t.Errorf("owner = %v, want alice from state", planned["owner"])
	}
	for _, attr := range []string{"id", "slug", "full_name", "html_url", "clone_url", "ssh_url"} {
		if planned[attr].IsKnown() {
			t.Errorf("%s = %v, want unknown after a rename", attr, planned[attr])
		}
	}
	for _, attr := range []string{"repo_id", "node_id", "private", "fork", "default_branch"} {
		if !planned[attr].IsKnown() {
			t.Errorf("%s is unknown, want the prior value kept", attr)
		}
	}
}

func TestRepositoryPlanVisibilityChangePlansPrivate(t *testing.T) {
	replace, planned := planRepository(t, map[string]tftypes.Value{
		"visibility": tftypes.NewValue(tftypes.String, "public"),
	})
	if len(replace) != 0 {
		t.Fatalf("visibility change must not force replacement, got RequiresReplace %v", replace)
	}
	if !planned["private"].Equal(tftypes.NewValue(tftypes.Bool, false)) {
		t.Errorf("private = %v, want false", planned["private"])
	}
	if !planned["slug"].Equal(tftypes.NewValue(tftypes.String, "old")) {
		t.Errorf("slug = %v, want unchanged", planned["slug"])
	}
}

func TestRepositoryPlanOwnerChangeForcesReplacement(t *testing.T) {
	replace, _ := planRepository(t, map[string]tftypes.Value{
		"owner": tftypes.NewValue(tftypes.String, "bob"),
	})
	want := tftypes.NewAttributePath().WithAttributeName("owner")
	for _, p := range replace {
		if p.Equal(want) {
			return
		}
	}
	t.Fatalf("RequiresReplace = %v, want owner", replace)
}

func TestRepositoryWithAppliedUpdate(t *testing.T) {
	state := repositoryResourceModel{
		ID: types.StringValue("alice/old"), FullName: types.StringValue("alice/old"), Slug: types.StringValue("old"),
		Name: types.StringValue("old"), Description: types.StringNull(), Visibility: types.StringValue("private"),
		DefaultBranch: types.StringValue("main"), HTMLURL: types.StringValue("h"),
	}
	plan := repositoryResourceModel{
		Name: types.StringValue("New"), Description: types.StringValue("d"), Visibility: types.StringValue("public"),
		DefaultBranch: types.StringUnknown(), HTMLURL: types.StringUnknown(),
	}

	got := state.withAppliedUpdate(plan, "alice", "new")
	if got.Slug.ValueString() != "new" || got.ID.ValueString() != "alice/new" || got.FullName.ValueString() != "alice/new" {
		t.Errorf("slug/id/full_name = %s/%s/%s", got.Slug, got.ID, got.FullName)
	}
	if got.Name.ValueString() != "New" || got.Description.ValueString() != "d" || got.Visibility.ValueString() != "public" {
		t.Errorf("name/description/visibility = %s/%s/%s", got.Name, got.Description, got.Visibility)
	}
	if got.DefaultBranch.ValueString() != "main" || got.HTMLURL.ValueString() != "h" {
		t.Errorf("unknown plan values must not overwrite state, got default_branch %s html_url %s", got.DefaultBranch, got.HTMLURL)
	}
	if state.Slug.ValueString() != "old" {
		t.Error("withAppliedUpdate must not modify the receiver")
	}
}
