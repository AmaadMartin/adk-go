# ArtifactVersion metadata

`Service.GetArtifactVersion` describes one stored version of an artifact without
reading its bytes. Reach for it when you need the identity, the type or the age
of an artifact, and not its payload.

## Introduction

An artifact payload can be large. Listing what a session produced, or showing a
user when a report was generated, does not need those bytes. `GetArtifactVersion`
returns an `ArtifactVersion` record instead, so a caller pays for the metadata
only.

The record has the same shape for every backend. An app developed against
`artifact.InMemoryService` and deployed against `gcsartifact` reads the same
fields, and only the `CanonicalURI` scheme differs. This matters most for a
server that serializes the record: no field is empty because of the backend you
chose.

## Get started

```go
package main

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
)

func main() {
	ctx := context.Background()
	srv := artifact.InMemoryService()

	if _, err := srv.Save(ctx, &artifact.SaveRequest{
		AppName: "app0", UserID: "user0", SessionID: "123", FileName: "report.md",
		Part: genai.NewPartFromBytes([]byte("# otters"), "text/markdown"),
	}); err != nil {
		log.Fatal(err)
	}

	resp, err := srv.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
		AppName: "app0", UserID: "user0", SessionID: "123", FileName: "report.md",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.ArtifactVersion.Version)
	fmt.Println(resp.ArtifactVersion.CanonicalURI)
	fmt.Println(resp.ArtifactVersion.MimeType)
}
```

That program prints:

```
1
memory://apps/app0/users/user0/sessions/123/artifacts/report.md/versions/1
text/markdown
```

Leave `Version` unset, as above, to get the latest version. Set it to read one
exact version; an unknown version returns an error that matches `fs.ErrNotExist`.

## Fields

| Field | What it holds |
| --- | --- |
| `Version` | The version that was read. Version numbers start at 1. |
| `CanonicalURI` | A string that identifies this version in its backend. |
| `CustomMetadata` | A map, never nil. |
| `CreateTime` | When the version was saved. |
| `MimeType` | The part's MIME type, or `text/plain` when the part carries none. |

## Canonical URI

The in-memory service builds the URI at save time and stores it. A session
scoped artifact names its session:

```
memory://apps/<app>/users/<user>/sessions/<session>/artifacts/<file>/versions/<n>
```

A file name that starts with `user:` is scoped to the user, not to the session,
so its URI omits the session segment and keeps the prefix in the name:

```
memory://apps/<app>/users/<user>/artifacts/user:document.pdf/versions/1
```

Because the URI is stored, a user scoped artifact reports the same URI whichever
session reads it.

`gcsartifact` reports the object's media link, and falls back to
`gs://<bucket>/<object>` when the link is empty.

## Create time in tests

The in-memory service reads the clock through `platform.Now`, so a test can fix
the time it records:

```go
fixed := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
ctx := platform.WithTimeProvider(t.Context(), func() time.Time { return fixed })
```

Every artifact saved with that context, or a context derived from it, reports
`fixed` as its `CreateTime`.

## Limitations

*   **`CustomMetadata` is always empty.** `SaveRequest` has no field for it, so
    no backend can store any. The map is non-nil so that callers and JSON
    encoders need no nil check.
*   **`Versions` returns numbers, not records.** To describe several versions,
    call `GetArtifactVersion` once per version.
