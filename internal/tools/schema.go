package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// declaredSchemas holds the repositories schema the team files declare,
// compiled once per document: the dry run after a commit that left the
// schema as it was validates with the compiled schema it has.
type declaredSchemas struct {
	mu     sync.Mutex
	doc    string
	schema *reposetup.Schema
}

// compiled is doc's schema, compiled when it differs from the last one.
func (c *declaredSchemas) compiled(doc []byte) (*reposetup.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schema != nil && c.doc == string(doc) {
		return c.schema, nil
	}
	s, err := reposetup.CompileSchema(doc, reposetup.SchemaOriginGitHub)
	if err != nil {
		return nil, err
	}
	c.doc, c.schema = string(doc), s
	return s, nil
}

// schemaReader is the team-files repository a creation's dry run reads the
// declared schema from: as the person when the call carries their token,
// else as the inventory App; nil when neither can read it.
func (t *tools) schemaReader(p *person) *teamfiles.Repo {
	if p != nil {
		return &p.repo
	}
	if repo, err := t.unattended(); err == nil {
		return &repo
	}
	return nil
}

// validatorSchema is the repositories schema a write's dry run validates
// with, the way the collector validates the entry once it merges: the one
// the team files declare, read from repo at its ref. The engine's embedded
// copy (Deps.Schema) validates only when the team files declare none, or
// when nothing reads them (repo nil: a dry run with neither the person's
// token nor the inventory App). A read or compile failure is the dry run's
// error: nothing stands in for a schema that is there.
func (t *tools) validatorSchema(ctx context.Context, repo *teamfiles.Repo) (*reposetup.Schema, error) {
	if repo != nil {
		doc, err := repo.Schema(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the repositories schema the team files declare: %w", err)
		}
		if doc != nil {
			s, err := t.schemas.compiled(doc)
			if err != nil {
				return nil, fmt.Errorf("%s at %s: %w", reposetup.SchemaPath, repo.Slug(), err)
			}
			return s, nil
		}
	}
	if t.d.Schema == nil {
		return nil, ErrNoSchema
	}
	return t.d.Schema, nil
}
