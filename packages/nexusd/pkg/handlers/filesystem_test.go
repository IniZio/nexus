package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	rpckit "github.com/nexus/nexus/packages/nexusd/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/nexusd/pkg/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleReadFile_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleReadFile(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleReadFile_Success(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	testFile := "test.txt"
	content := "hello world"
	err = os.WriteFile(filepath.Join(ws.Path(), testFile), []byte(content), 0644)
	require.NoError(t, err)

	params := `{"path": "test.txt"}`
	result, rpcErr := HandleReadFile(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, content, result.Content)
	assert.Equal(t, int64(len(content)), result.Size)
}

func TestHandleReadFile_NotFound(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "nonexistent.txt"}`
	result, rpcErr := HandleReadFile(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrFileNotFound.Code, rpcErr.Code)
}

func TestHandleReadFile_InvalidPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "/etc/passwd"}`
	result, rpcErr := HandleReadFile(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidPath.Code, rpcErr.Code)
}

func TestHandleReadFile_PathTraversal(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "../go.mod"}`
	result, rpcErr := HandleReadFile(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidPath.Code, rpcErr.Code)
}

func TestHandleWriteFile_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleWriteFile(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleWriteFile_EmptyPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "", "content": "test"}`
	result, rpcErr := HandleWriteFile(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleWriteFile_Success(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	content := "test content"
	params := `{"path": "newfile.txt", "content": "test content"}`
	result, rpcErr := HandleWriteFile(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.OK)
	assert.Equal(t, "newfile.txt", result.Path)
	assert.Equal(t, int64(len(content)), result.Size)

	fileContent, err := os.ReadFile(filepath.Join(ws.Path(), "newfile.txt"))
	require.NoError(t, err)
	assert.Equal(t, content, string(fileContent))
}

func TestHandleWriteFile_InvalidPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "/etc/passwd", "content": "test"}`
	result, rpcErr := HandleWriteFile(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidPath.Code, rpcErr.Code)
}

func TestHandleWriteFile_NestedPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "dir/subdir/file.txt", "content": "nested content"}`
	result, rpcErr := HandleWriteFile(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.OK)

	fileContent, err := os.ReadFile(filepath.Join(ws.Path(), "dir/subdir/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested content", string(fileContent))
}

func TestHandleWriteFile_Base64(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "encoded.txt", "content": "dGVzdA==", "encoding": "base64"}`
	result, rpcErr := HandleWriteFile(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.OK)
}

func TestHandleExists_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleExists(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleExists_True(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	testFile := "exists.txt"
	err = os.WriteFile(filepath.Join(ws.Path(), testFile), []byte("content"), 0644)
	require.NoError(t, err)

	params := `{"path": "exists.txt"}`
	result, rpcErr := HandleExists(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.Exists)
}

func TestHandleExists_False(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "nonexistent.txt"}`
	result, rpcErr := HandleExists(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.False(t, result.Exists)
}

func TestHandleReaddir_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleReaddir(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleReaddir_Success(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	os.MkdirAll(filepath.Join(ws.Path(), "subdir"), 0755)
	os.WriteFile(filepath.Join(ws.Path(), "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(ws.Path(), "file2.txt"), []byte("content2"), 0644)

	params := `{"path": "."}`
	result, rpcErr := HandleReaddir(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Entries), 2)
}

func TestHandleReaddir_NotFound(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "nonexistent"}`
	result, rpcErr := HandleReaddir(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrFileNotFound.Code, rpcErr.Code)
}

func TestHandleMkdir_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleMkdir(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleMkdir_EmptyPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": ""}`
	result, rpcErr := HandleMkdir(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleMkdir_Success(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "newdir"}`
	result, rpcErr := HandleMkdir(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.OK)

	info, err := os.Stat(filepath.Join(ws.Path(), "newdir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestHandleMkdir_Recursive(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "a/b/c", "recursive": true}`
	result, rpcErr := HandleMkdir(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)

	info, err := os.Stat(filepath.Join(ws.Path(), "a/b/c"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestHandleRm_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleRm(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleRm_EmptyPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": ""}`
	result, rpcErr := HandleRm(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleRm_File(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	testFile := filepath.Join(ws.Path(), "to-delete.txt")
	err = os.WriteFile(testFile, []byte("content"), 0644)
	require.NoError(t, err)

	params := `{"path": "to-delete.txt"}`
	result, rpcErr := HandleRm(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.True(t, result.OK)

	_, err = os.Stat(testFile)
	assert.True(t, os.IsNotExist(err))
}

func TestHandleRm_NotFound(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "nonexistent.txt"}`
	result, rpcErr := HandleRm(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrFileNotFound.Code, rpcErr.Code)
}

func TestHandleRm_Recursive(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	subDir := filepath.Join(ws.Path(), "todelete")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("content"), 0644)

	params := `{"path": "todelete", "recursive": true}`
	result, rpcErr := HandleRm(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)

	_, err = os.Stat(subDir)
	assert.True(t, os.IsNotExist(err))
}

func TestHandleStat_InvalidParams(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	result, rpcErr := HandleStat(context.Background(), []byte("invalid json"), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidParams.Code, rpcErr.Code)
}

func TestHandleStat_File(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	testFile := filepath.Join(ws.Path(), "statfile.txt")
	content := "test content"
	err = os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	params := `{"path": "statfile.txt"}`
	result, rpcErr := HandleStat(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "statfile.txt", result.Name)
	assert.False(t, result.IsDir)
	assert.Equal(t, int64(len(content)), result.Size)
}

func TestHandleStat_Dir(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	subDir := filepath.Join(ws.Path(), "statdir")
	err = os.MkdirAll(subDir, 0755)
	require.NoError(t, err)

	params := `{"path": "statdir"}`
	result, rpcErr := HandleStat(context.Background(), []byte(params), ws)
	require.NoError(t, err)
	assert.Nil(t, rpcErr)
	assert.NotNil(t, result)
	assert.Equal(t, "statdir", result.Name)
	assert.True(t, result.IsDir)
}

func TestHandleStat_NotFound(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "nonexistent.txt"}`
	result, rpcErr := HandleStat(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrFileNotFound.Code, rpcErr.Code)
}

func TestHandleStat_InvalidPath(t *testing.T) {
	ws, err := workspace.NewWorkspace(t.TempDir())
	require.NoError(t, err)

	params := `{"path": "/etc/passwd"}`
	result, rpcErr := HandleStat(context.Background(), []byte(params), ws)
	assert.Nil(t, result)
	assert.NotNil(t, rpcErr)
	assert.Equal(t, rpckit.ErrInvalidPath.Code, rpcErr.Code)
}

func TestReadFileParams_JSONMarshaling(t *testing.T) {
	params := ReadFileParams{
		Path:     "test.txt",
		Encoding: "utf8",
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"path":"test.txt"`)
}

func TestWriteFileParams_JSONMarshaling(t *testing.T) {
	params := WriteFileParams{
		Path:     "test.txt",
		Content:  "hello",
		Encoding: "utf8",
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"path":"test.txt"`)
	assert.Contains(t, string(data), `"content":"hello"`)
}

func TestDirEntry_JSONMarshaling(t *testing.T) {
	entry := DirEntry{
		Name:  "test.txt",
		Path:  "/path/test.txt",
		IsDir: false,
		Size:  100,
		Mode:  "-rw-r--r--",
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"name":"test.txt"`)
	assert.Contains(t, string(data), `"is_dir":false`)
}
