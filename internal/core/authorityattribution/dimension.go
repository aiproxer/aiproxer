package authorityattribution

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// DimensionKey is a deterministic encoding of Dimensions.
type DimensionKey string

// Dimensions are safe attribution fields for rule matching and identity.
type Dimensions struct {
	Principal    scope.Value
	Credential   scope.Value
	Tenant       scope.Value
	Organization scope.Value
	Workspace    scope.Value
	Project      scope.Value
	Department   scope.Value
	CostCenter   scope.Value
	Backend      scope.Value
	Model        scope.Value
	Route        scope.Value
	PolicyLabels map[string]scope.Value
}

// Key returns a deterministic dimension encoding. Invalid label keys are omitted.
func (d Dimensions) Key() DimensionKey {
	key, _ := d.KeyErr()
	return key
}

// KeyErr returns a deterministic dimension encoding or an error for unsafe labels.
func (d Dimensions) KeyErr() (DimensionKey, error) {
	var b strings.Builder
	write := func(name string, value scope.Value) {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(encodeScopeValue(value))
		b.WriteByte('|')
	}
	write("principal", d.Principal)
	write("credential", d.Credential)
	write("tenant", d.Tenant)
	write("organization", d.Organization)
	write("workspace", d.Workspace)
	write("project", d.Project)
	write("department", d.Department)
	write("cost_center", d.CostCenter)
	write("backend", d.Backend)
	write("model", d.Model)
	write("route", d.Route)

	labelKeys := make([]string, 0, len(d.PolicyLabels))
	for key := range d.PolicyLabels {
		if !IsSafeLabelKey(key) {
			return "", fmt.Errorf("authorityattribution: invalid policy label key %q", key)
		}
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		b.WriteString("label.")
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(encodeScopeValue(d.PolicyLabels[key]))
		b.WriteByte('|')
	}
	return DimensionKey(strings.TrimSuffix(b.String(), "|")), nil
}

func encodeScopeValue(v scope.Value) string {
	if v.IsUnknown() {
		return "u"
	}
	return "k:" + url.QueryEscape(v.String())
}

// IsSafeLabelKey reports whether key is safe as a policy-label dimension key.
func IsSafeLabelKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// DimensionMatcher matches one attribution field.
type DimensionMatcher struct {
	Value        scope.Value
	MatchUnknown bool
}

// Matches reports whether actual satisfies the matcher.
func (m DimensionMatcher) Matches(actual scope.Value) bool {
	if m.MatchUnknown {
		return actual.IsUnknown()
	}
	if m.Value.IsUnknown() {
		return true
	}
	return m.Value.Equal(actual)
}

// DimensionsMatcher is a safe multi-dimension rule matcher.
type DimensionsMatcher struct {
	Principal    DimensionMatcher
	Credential   DimensionMatcher
	Tenant       DimensionMatcher
	Organization DimensionMatcher
	Workspace    DimensionMatcher
	Project      DimensionMatcher
	Department   DimensionMatcher
	CostCenter   DimensionMatcher
	Backend      DimensionMatcher
	Model        DimensionMatcher
	Route        DimensionMatcher
	Labels       map[string]DimensionMatcher
}

// Matches reports whether actual satisfies every configured matcher.
func (m DimensionsMatcher) Matches(actual Dimensions) bool {
	if !m.Principal.Matches(actual.Principal) {
		return false
	}
	if !m.Credential.Matches(actual.Credential) {
		return false
	}
	if !m.Tenant.Matches(actual.Tenant) {
		return false
	}
	if !m.Organization.Matches(actual.Organization) {
		return false
	}
	if !m.Workspace.Matches(actual.Workspace) {
		return false
	}
	if !m.Project.Matches(actual.Project) {
		return false
	}
	if !m.Department.Matches(actual.Department) {
		return false
	}
	if !m.CostCenter.Matches(actual.CostCenter) {
		return false
	}
	if !m.Backend.Matches(actual.Backend) {
		return false
	}
	if !m.Model.Matches(actual.Model) {
		return false
	}
	if !m.Route.Matches(actual.Route) {
		return false
	}
	for key, matcher := range m.Labels {
		if !IsSafeLabelKey(key) {
			return false
		}
		if !matcher.Matches(actual.PolicyLabels[key]) {
			return false
		}
	}
	return true
}

// DimensionsFromScope projects a principal scope view onto dimensions.
func DimensionsFromScope(view scope.PrincipalScopeView) Dimensions {
	out := Dimensions{
		Principal:    view.PrincipalID,
		Credential:   view.CredentialID,
		Tenant:       view.TenantID,
		Organization: view.OrganizationID,
		Workspace:    view.WorkspaceID,
		Project:      view.ProjectID,
		Department:   view.DepartmentID,
		CostCenter:   view.CostCenterID,
	}
	if len(view.PolicyLabels) > 0 {
		out.PolicyLabels = make(map[string]scope.Value, len(view.PolicyLabels))
		for key, value := range view.PolicyLabels {
			if !IsSafeLabelKey(key) {
				continue
			}
			out.PolicyLabels[key] = scope.Known(value)
		}
	}
	return out
}
