package themes

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestServiceCreateAutoSlugAndCreatorGuard(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewThemeStore())

	const owner = int64(1001)
	a, err := svc.Create(ctx, domain.ThemeSpec{CreatorUserID: owner, Title: "A", DocumentID: 50})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if a.Slug == "" || a.ID == 0 || a.AccessHash == 0 {
		t.Fatalf("create a = %+v, want auto slug + ids", a)
	}
	if !a.IsCreator(owner) {
		t.Fatalf("creator mismatch")
	}

	// Two empty-slug creates yield different automatic slugs.
	b, err := svc.Create(ctx, domain.ThemeSpec{CreatorUserID: owner, Title: "B"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if b.Slug == a.Slug {
		t.Fatalf("auto slugs collided: %q", a.Slug)
	}

	// A non-creator cannot edit.
	if _, err := svc.Update(ctx, 2002, domain.ThemeRef{ID: a.ID, AccessHash: a.AccessHash}, domain.ThemeUpdate{Title: strptr("hacked")}); !errors.Is(err, domain.ErrThemeInvalid) {
		t.Fatalf("non-creator update err = %v, want ErrThemeInvalid", err)
	}

	// The creator can edit title + document.
	newTitle := "A2"
	newDoc := int64(99)
	updated, err := svc.Update(ctx, owner, domain.ThemeRef{ID: a.ID, AccessHash: a.AccessHash}, domain.ThemeUpdate{Title: &newTitle, DocumentID: &newDoc})
	if err != nil {
		t.Fatalf("creator update: %v", err)
	}
	if updated.Title != "A2" || updated.DocumentID != 99 {
		t.Fatalf("update result = %+v, want title A2 doc 99", updated)
	}

	// install count + list.
	if err := svc.Install(ctx, owner, domain.ThemeRef{ID: a.ID, AccessHash: a.AccessHash}, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, ok, _ := svc.Get(ctx, domain.ThemeRef{Slug: a.Slug})
	if !ok || got.InstallsCount != 1 {
		t.Fatalf("after install installs = %d ok=%v, want 1", got.InstallsCount, ok)
	}
	list, _ := svc.ListInstalled(ctx, owner)
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("installed list = %+v, want [a]", list)
	}

	// Unknown reference → ErrThemeInvalid.
	if err := svc.Install(ctx, owner, domain.ThemeRef{Slug: "nope"}, false); !errors.Is(err, domain.ErrThemeInvalid) {
		t.Fatalf("install unknown err = %v, want ErrThemeInvalid", err)
	}

	forged := domain.ThemeRef{ID: a.ID, AccessHash: a.AccessHash + 1}
	if _, ok, err := svc.Get(ctx, forged); err != nil || ok {
		t.Fatalf("get forged access hash = ok %v err %v, want false/nil", ok, err)
	}
	if err := svc.Save(ctx, owner, forged); !errors.Is(err, domain.ErrThemeInvalid) {
		t.Fatalf("save forged access hash err = %v, want ErrThemeInvalid", err)
	}
	if err := svc.Install(ctx, owner, forged, true); !errors.Is(err, domain.ErrThemeInvalid) {
		t.Fatalf("install forged access hash err = %v, want ErrThemeInvalid", err)
	}
	if _, err := svc.Update(ctx, owner, forged, domain.ThemeUpdate{Title: strptr("forged")}); !errors.Is(err, domain.ErrThemeNotFound) {
		t.Fatalf("update forged access hash err = %v, want ErrThemeNotFound", err)
	}
}

func strptr(s string) *string { return &s }
