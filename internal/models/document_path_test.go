package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

func TestParsePath_Depth(t *testing.T) {
	type want struct {
		cat, sub, slug string
		hasSub         bool
	}
	cases := map[string]want{
		"root":                              {"misc", "", "root", false},
		"prompts/persona":                   {"prompts", "", "persona", false},
		"prompts/derpy/persona":             {"prompts", "derpy", "persona", true},
		"prompts/a11s/platform/root":        {"prompts", "a11s/platform", "root", true},
		"projects/a11s/platform/team/state": {"projects", "a11s/platform/team", "state", true},
	}
	for path, w := range cases {
		cat, sub, slug := models.ParsePath(path)
		require.Equal(t, w.cat, cat, path)
		require.Equal(t, w.slug, slug, path)
		if w.hasSub {
			require.NotNil(t, sub, path)
			require.Equal(t, w.sub, *sub, path)
		} else {
			require.Nil(t, sub, path)
		}
	}
}

func TestValidateDocumentPath_Subcategory(t *testing.T) {
	require.NoError(t, models.ValidateDocumentPath("prompts", "root", nil))
	for _, s := range []string{"go", "a11s/platform", "a11s/platform/infra"} {
		s := s
		require.NoError(t, models.ValidateDocumentPath("prompts", "root", &s), s)
	}
	for _, s := range []string{"a11s//platform", "/a11s", "a11s/", "a11s/ bad", "a11s/."} {
		s := s
		require.Error(t, models.ValidateDocumentPath("prompts", "root", &s), s)
	}
}

func TestInferDocType_DepthIndependent(t *testing.T) {
	deep := "a11s/platform"
	require.Equal(t, models.DocTypePrompt, models.InferDocType("prompts", &deep, "root"))
	require.Equal(t, models.DocTypeProjectState, models.InferDocType("projects", &deep, "state"))
	require.Equal(t, models.DocTypeLearning, models.InferDocType("learnings", &deep, "gorm"))
}
