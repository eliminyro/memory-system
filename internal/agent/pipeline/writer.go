package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/eliminyro/memory-system/internal/agent/mcpclient"
	"github.com/eliminyro/memory-system/internal/models"
)

func Write(ctx context.Context, mcp *mcpclient.Client, reviewed []ReviewedCandidate) (accepted, merged, rejected int, err error) {
	var errs []error
	for _, rc := range reviewed {
		switch rc.Verdict {
		case VerdictAccept:
			category, subcategory, slug := models.ParsePath(rc.Path)
			content := formatContent(rc.Heading, rc.Content)
			if storeErr := mcp.StoreMemory(ctx, category, subcategory, slug, content); storeErr != nil {
				slog.Error("failed to store candidate", "path", rc.Path, "error", storeErr)
				errs = append(errs, storeErr)
				continue
			}
			slog.Info("stored new knowledge", "path", rc.Path, "heading", rc.Heading)
			accepted++

		case VerdictMerge:
			if rc.MergeTarget == "" {
				slog.Warn("merge verdict without target, storing as new", "path", rc.Path)
				category, subcategory, slug := models.ParsePath(rc.Path)
				content := formatContent(rc.Heading, rc.Content)
				if storeErr := mcp.StoreMemory(ctx, category, subcategory, slug, content); storeErr != nil {
					slog.Error("failed to store merged candidate", "path", rc.Path, "error", storeErr)
					errs = append(errs, storeErr)
					continue
				}
				accepted++
				continue
			}
			if mergeErr := mcp.UpdateSection(ctx, rc.MergeTarget, rc.Content); mergeErr != nil {
				slog.Error("failed to merge candidate", "path", rc.Path, "target", rc.MergeTarget, "error", mergeErr)
				errs = append(errs, mergeErr)
				continue
			}
			slog.Info("merged knowledge", "path", rc.Path, "target", rc.MergeTarget)
			merged++

		case VerdictReject:
			slog.Info("rejected candidate", "path", rc.Path, "reason", rc.Reason)
			rejected++
		}
	}
	return accepted, merged, rejected, errors.Join(errs...)
}

func formatContent(heading, content string) string {
	var b strings.Builder
	if heading != "" {
		b.WriteString("# ")
		b.WriteString(heading)
		b.WriteString("\n\n")
	}
	b.WriteString(content)
	return b.String()
}
