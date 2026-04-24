/*
 * Unit tests for the ProcFS abstraction used by the bare-metal user mapper.
 * Feature 001-multi-user-gpu-util, task T015.
 */

package transformation

import (
	sysOS "os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeProcDir lays out <root>/<pid>/{status,environ} for use by realProcFS.
func writeProcDir(t *testing.T, root string, pid uint32, status, environ []byte) {
	t.Helper()
	dir := filepath.Join(root, itoa(uint64(pid)))
	require.NoError(t, sysOS.MkdirAll(dir, 0o755))
	if status != nil {
		require.NoError(t, sysOS.WriteFile(filepath.Join(dir, "status"), status, 0o644))
	}
	if environ != nil {
		require.NoError(t, sysOS.WriteFile(filepath.Join(dir, "environ"), environ, 0o644))
	}
}

func itoa(u uint64) string {
	return fmtInt(u)
}

// fmtInt avoids an extra import purely for strconv.Itoa in tests.
func fmtInt(u uint64) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}

func TestRealProcFS_ReadStatus_OK(t *testing.T) {
	root := t.TempDir()
	writeProcDir(t, root, 42, []byte("Name:\tbash\nUmask:\t0022\nState:\tS (sleeping)\nTgid:\t42\nNgid:\t0\nPid:\t42\nPPid:\t1\nUid:\t1000\t1000\t1000\t1000\n"), nil)
	fs := NewProcFSAt(root)
	uid, err := fs.ReadStatus(42)
	require.NoError(t, err)
	require.Equal(t, uint32(1000), uid)
}

func TestRealProcFS_ReadStatus_Missing(t *testing.T) {
	root := t.TempDir()
	fs := NewProcFSAt(root)
	_, err := fs.ReadStatus(9999)
	require.Error(t, err)
}

func TestRealProcFS_ReadStatus_MalformedUID(t *testing.T) {
	root := t.TempDir()
	writeProcDir(t, root, 11, []byte("Name:\tbash\nUid:\tnot-a-number\n"), nil)
	fs := NewProcFSAt(root)
	_, err := fs.ReadStatus(11)
	require.Error(t, err)
}

func TestRealProcFS_ReadStatus_NoUIDLine(t *testing.T) {
	root := t.TempDir()
	writeProcDir(t, root, 22, []byte("Name:\tbash\nState:\tS\n"), nil)
	fs := NewProcFSAt(root)
	_, err := fs.ReadStatus(22)
	require.Error(t, err)
}

func TestRealProcFS_ReadEnviron_OK(t *testing.T) {
	root := t.TempDir()
	// NUL-separated tokens, trailing NUL is fine.
	env := []byte("PATH=/usr/bin\x00PROJECT=llm-training\x00HOME=/home/alice\x00")
	writeProcDir(t, root, 7, nil, env)
	fs := NewProcFSAt(root)
	val, err := fs.ReadEnviron(7, "PROJECT")
	require.NoError(t, err)
	require.Equal(t, "llm-training", val)
}

func TestRealProcFS_ReadEnviron_KeyMissing(t *testing.T) {
	root := t.TempDir()
	env := []byte("PATH=/usr/bin\x00HOME=/home/alice\x00")
	writeProcDir(t, root, 8, nil, env)
	fs := NewProcFSAt(root)
	val, err := fs.ReadEnviron(8, "PROJECT")
	require.NoError(t, err, "absent key must not be an error")
	require.Equal(t, "", val)
}

func TestRealProcFS_ReadEnviron_FileMissing(t *testing.T) {
	root := t.TempDir()
	fs := NewProcFSAt(root)
	_, err := fs.ReadEnviron(12345, "PROJECT")
	require.Error(t, err)
}

func TestRealProcFS_ReadEnviron_EqualsInValue(t *testing.T) {
	root := t.TempDir()
	env := []byte("PROJECT=a=b=c\x00")
	writeProcDir(t, root, 33, nil, env)
	fs := NewProcFSAt(root)
	val, err := fs.ReadEnviron(33, "PROJECT")
	require.NoError(t, err)
	require.Equal(t, "a=b=c", val)
}

func TestRealProcFS_ReadEnviron_EmptyFile(t *testing.T) {
	root := t.TempDir()
	writeProcDir(t, root, 44, nil, []byte{})
	fs := NewProcFSAt(root)
	val, err := fs.ReadEnviron(44, "PROJECT")
	require.NoError(t, err)
	require.Equal(t, "", val)
}
