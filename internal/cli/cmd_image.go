package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "image",
		Summary: "Manage guest images (build|ls|prune)",
		Run:     runImage,
	})
}

// ── JSON shapes (stable machine contract) ────────────────────────────────────
//
// These types form a versioned API surface. Do not rename fields or remove
// types without bumping schemaVersion in output.go.

// imageBuiltJSON is emitted by "image build" on success.
type imageBuiltJSON struct {
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
	Size   int64  `json:"size"`
}

// imageInfoJSON carries per-image metadata, used inside imageListJSON.
type imageInfoJSON struct {
	Digest    string    `json:"digest"`
	Ref       string    `json:"ref"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// imageListJSON is emitted by "image ls" on success.
type imageListJSON struct {
	Images []imageInfoJSON `json:"images"`
}

// imagePrunedJSON is emitted by "image prune" on success.
type imagePrunedJSON struct {
	Removed int `json:"removed"`
}

// ── top-level dispatch ────────────────────────────────────────────────────────

// runImage is the Command.Run function registered for the "image" verb.
// It constructs the production ImageService and delegates to runImageWithService.
// Tests call runImageWithService directly with an injected service instance.
func runImage(ctx context.Context, args []string, out *Output) error {
	svc, err := newImageService()
	if err != nil {
		out.EmitError(ErrCodeInternalError, err.Error())
		return nil
	}
	return runImageWithService(ctx, args, out, svc)
}

// runImageWithService handles subcommand dispatch. Tests call this directly.
func runImageWithService(ctx context.Context, args []string, out *Output, svc *service.ImageService) error {
	if len(args) == 0 {
		return &UsageError{Msg: "image: usage: image <build|ls|prune>"}
	}

	verb, verbArgs := args[0], args[1:]
	switch verb {
	case "build":
		return runImageBuild(ctx, verbArgs, out, svc)
	case "ls":
		return runImageList(ctx, verbArgs, out, svc)
	case "prune":
		return runImagePrune(ctx, verbArgs, out, svc)
	default:
		return &UsageError{Msg: fmt.Sprintf("image: unknown subcommand %q; valid: build ls prune", verb)}
	}
}

// ── build ─────────────────────────────────────────────────────────────────────

// runImageBuild handles: image build [--workspace <dir>] [--ref <tag>] [--base <oci-ref>]
func runImageBuild(ctx context.Context, args []string, out *Output, svc *service.ImageService) error {
	fs := flag.NewFlagSet("image build", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "path to workspace root containing .nexus/Containerfile (default: cwd)")
	ref := fs.String("ref", "", "human-readable tag stamped on the image, e.g. nexus3-base:20260807 (optional)")
	base := fs.String("base", "debian:bookworm-slim", "OCI base image reference")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "image build: " + err.Error()}
	}

	if *workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			return &UsageError{Msg: "image build: --workspace required (could not determine working directory: " + err.Error() + ")"}
		}
		*workspace = wd
	}

	req := builder.BuildRequest{
		BaseRef:      *base,
		WorkspaceDir: *workspace,
		Ref:          *ref,
	}
	img, err := svc.BuildImage(ctx, req)
	if err != nil {
		out.EmitError(ErrCodeInternalError, fmt.Sprintf("image build: %v", err))
		return nil
	}

	out.EmitSuccess("image.built", imageBuiltJSON{
		Digest: img.Digest.String(),
		Ref:    img.Ref,
		Size:   img.Size,
	}, fmt.Sprintf("built image %s (%d bytes)", img.Digest, img.Size))
	return nil
}

// ── ls ────────────────────────────────────────────────────────────────────────

// runImageList handles: image ls
func runImageList(ctx context.Context, _ []string, out *Output, svc *service.ImageService) error {
	imgs, err := svc.ListImages(ctx)
	if err != nil {
		out.EmitError(ErrCodeInternalError, fmt.Sprintf("image ls: %v", err))
		return nil
	}

	rows := make([]imageInfoJSON, len(imgs))
	for i, img := range imgs {
		rows[i] = toImageInfoJSON(img)
	}
	out.EmitSuccess("image.list", imageListJSON{Images: rows},
		fmt.Sprintf("%d image(s)", len(imgs)))
	return nil
}

// ── prune ─────────────────────────────────────────────────────────────────────

// runImagePrune handles: image prune
func runImagePrune(ctx context.Context, _ []string, out *Output, svc *service.ImageService) error {
	n, err := svc.PruneImages(ctx)
	if err != nil {
		out.EmitError(ErrCodeInternalError, fmt.Sprintf("image prune: %v", err))
		return nil
	}

	out.EmitSuccess("image.pruned", imagePrunedJSON{Removed: n},
		fmt.Sprintf("pruned %d image(s)", n))
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toImageInfoJSON(img domain.Image) imageInfoJSON {
	return imageInfoJSON{
		Digest:    img.Digest.String(),
		Ref:       img.Ref,
		Kind:      img.Kind.String(),
		Size:      img.Size,
		CreatedAt: img.CreatedAt,
	}
}

// newImageService constructs an ImageService for production use.
//
// Cache root: $XDG_STATE_HOME/nexus3/images (via store.DefaultRoot).
// Builder: nil — buildkitd connectivity is wired in a separate integration
// slice. BuildImage returns service.ErrNoBuilder until that slice is merged.
func newImageService() (*service.ImageService, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, fmt.Errorf("image: resolve state directory: %w", err)
	}
	cacheRoot := filepath.Join(root, "images")
	c, err := image.NewCache(cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("image: open cache: %w", err)
	}
	// Builder is nil: the builder VM integration slice wires it.
	return service.NewImageService(c, nil), nil
}
