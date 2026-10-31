// Package fossillib reads Fossil repositories.
//
// Fossil stores a repository in a single SQLite database. That database
// layout is an implementation detail and has changed between Fossil
// releases. This package therefore never reads the database directly. It
// asks the fossil executable for artifacts and parses the artifact text,
// which the Fossil project documents as its durable interchange format.
//
// The model is artifact-centric rather than revision-centric:
//
//   - a CommitObject is a check-in manifest artifact;
//   - a ManifestObject is the complete file list of a check-in, resolved
//     through B cards when the artifact is a delta manifest;
//   - a BlobObject is a content artifact (one version of one file);
//   - branches and tags are Fossil tags, some of which have special names.
//
// Read operations batch where Fossil offers a batch interface. Commit log
// pages use "fossil json timeline" and whole-tree lookups reuse a parsed
// manifest, so a page normally costs one fossil process instead of one
// process per object. Parsed artifacts are cached per repository handle
// until Refresh is called.
//
// Operations that Fossil only exposes on a check-out (annotate/blame,
// committing, merging) run in a temporary check-out. The repository's own
// working directory, if any, is never touched.
package fossillib
