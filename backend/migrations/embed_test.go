package migrations

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedMigrationsExcludeMetadataAndPreserveSQL(t *testing.T) {
	// Inspect the actual compiled embed.FS, not a separately filtered disk list.
	embedded, err := fs.Glob(FS, "*")
	require.NoError(t, err)
	require.NotEmpty(t, embedded)
	_, err = FS.ReadFile("._001_init.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)

	onDisk, err := filepath.Glob("[0-9]*.sql")
	require.NoError(t, err)
	require.Equal(t, onDisk, embedded, "embedded migrations must exactly match numbered SQL files")
	for _, name := range embedded {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(name)
			require.NoError(t, err)
			got, err := FS.ReadFile(name)
			require.NoError(t, err)
			require.Equal(t, want, got, "packing must not alter SQL bytes or checksums")
		})
	}
}
