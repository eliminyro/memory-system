package authzseed

import (
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
)

func TestTupleConstructors(t *testing.T) {
	tid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	did := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	cases := []struct {
		name string
		got  authz.Tuple
		want authz.Tuple
	}{
		{
			"tenant system edge",
			TenantSystemEdge(tid),
			authz.Tuple{ObjectType: authz.TypeTenant, ObjectID: tid.String(), Relation: authz.RelSystem, SubjectType: authz.TypeSystem, SubjectID: authz.SystemObjectID},
		},
		{
			"tenant member",
			TenantMember(tid, "svc:x"),
			authz.Tuple{ObjectType: authz.TypeTenant, ObjectID: tid.String(), Relation: authz.RelMember, SubjectType: authz.TypeUser, SubjectID: "svc:x"},
		},
		{
			"tenant admin",
			TenantAdmin(tid, "u1"),
			authz.Tuple{ObjectType: authz.TypeTenant, ObjectID: tid.String(), Relation: authz.RelAdmin, SubjectType: authz.TypeUser, SubjectID: "u1"},
		},
		{
			"document tenant edge",
			DocumentTenantEdge(did, tid),
			authz.Tuple{ObjectType: authz.TypeDocument, ObjectID: did.String(), Relation: authz.RelTenant, SubjectType: authz.TypeTenant, SubjectID: tid.String()},
		},
		{
			"system admin",
			SystemAdmin("u2"),
			authz.Tuple{ObjectType: authz.TypeSystem, ObjectID: authz.SystemObjectID, Relation: authz.RelAdmin, SubjectType: authz.TypeUser, SubjectID: "u2"},
		},
		{
			"common pool wildcard",
			CommonPoolViewerWildcard(),
			authz.Tuple{ObjectType: authz.TypeTenant, ObjectID: models.BootstrapTenantID.String(), Relation: authz.RelViewer, SubjectType: authz.TypeUser, SubjectID: authz.Wildcard},
		},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
}

func TestAPIKeySubjectID(t *testing.T) {
	tid := uuid.New()
	// No explicit subject -> tenant service principal.
	if got := APIKeySubjectID(models.APIKey{TenantID: tid}); got != "svc:"+tid.String() {
		t.Fatalf("nil subject: got %q, want svc:%s", got, tid)
	}
	// Explicit subject -> verbatim.
	sub := "tu-explicit"
	if got := APIKeySubjectID(models.APIKey{TenantID: tid, SubjectID: &sub}); got != sub {
		t.Fatalf("explicit subject: got %q, want %q", got, sub)
	}
}
