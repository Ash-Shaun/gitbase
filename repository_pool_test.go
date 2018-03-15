package gitquery

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/src-d/go-git-fixtures.v3"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-mysql-server.v0/sql"
)

func TestRepository(t *testing.T) {
	require := require.New(t)

	gitRepo := &git.Repository{}
	repo := NewRepository("identifier", gitRepo)

	require.Equal("identifier", repo.ID)
	require.Equal(gitRepo, repo.Repo)

	repo = NewRepository("/other/path", nil)

	require.Equal("/other/path", repo.ID)
	require.Nil(repo.Repo)
}

func TestRepositoryPoolBasic(t *testing.T) {
	require := require.New(t)

	pool := NewRepositoryPool()

	// GetPos

	repo, err := pool.GetPos(0)
	require.Nil(repo)
	require.Equal(io.EOF, err)

	// Add and GetPos

	pool.Add("0", "/directory/should/not/exist")
	repo, err = pool.GetPos(0)
	require.NotNil(err)

	_, err = pool.GetPos(1)
	require.Equal(io.EOF, err)

	path := fixtures.Basic().ByTag("worktree").One().Worktree().Root()

	pool.Add("1", path)
	repo, err = pool.GetPos(1)
	require.Nil(err)
	require.Equal("1", repo.ID)
	require.NotNil(repo.Repo)

	_, err = pool.GetPos(0)
	require.Equal(git.ErrRepositoryNotExists, err)
	_, err = pool.GetPos(2)
	require.Equal(io.EOF, err)
}

func TestRepositoryPoolGit(t *testing.T) {
	require := require.New(t)

	path := fixtures.Basic().ByTag("worktree").One().Worktree().Root()
	dirName := filepath.Base(path)

	pool := NewRepositoryPool()
	id, err := pool.AddGit(path)
	require.Equal(dirName, id)
	require.Nil(err)

	repo, err := pool.GetPos(0)
	require.Equal(dirName, repo.ID)
	require.NotNil(repo.Repo)
	require.Nil(err)

	iter, err := repo.Repo.CommitObjects()
	require.Nil(err)

	count := 0

	for {
		commit, err := iter.Next()
		if err != nil {
			break
		}

		require.NotNil(commit)

		count++
	}

	require.Equal(9, count)
}

func TestRepositoryPoolIterator(t *testing.T) {
	require := require.New(t)

	path := fixtures.Basic().ByTag("worktree").One().Worktree().Root()

	pool := NewRepositoryPool()
	pool.Add("0", path)
	pool.Add("1", path)

	iter, err := pool.RepoIter()
	require.Nil(err)

	count := 0

	for {
		repo, err := iter.Next()
		if err != nil {
			require.Equal(io.EOF, err)
			break
		}

		require.NotNil(repo)
		require.Equal(strconv.Itoa(count), repo.ID)

		count++
	}

	require.Equal(2, count)
}

type testCommitIter struct {
	iter object.CommitIter
}

func (d *testCommitIter) NewIterator(
	repo *Repository,
) (RowRepoIter, error) {
	iter, err := repo.Repo.CommitObjects()
	if err != nil {
		return nil, err
	}

	return &testCommitIter{iter: iter}, nil
}

func (d *testCommitIter) Next() (sql.Row, error) {
	_, err := d.iter.Next()
	return nil, err
}

func (d *testCommitIter) Close() error {
	if d.iter != nil {
		d.iter.Close()
	}

	return nil
}

func testRepoIter(num int, require *require.Assertions, pool *RepositoryPool) {
	cIter := &testCommitIter{}

	repoIter, err := NewRowRepoIter(pool, cIter)
	require.Nil(err)

	count := 0
	for {
		row, err := repoIter.Next()
		if err != nil {
			require.Equal(io.EOF, err)
			break
		}

		require.Nil(row)

		count++
	}

	// 9 is the number of commits from the test repo
	require.Equal(9*num, count)
}

func TestRepositoryRowIterator(t *testing.T) {
	require := require.New(t)

	path := fixtures.Basic().ByTag("worktree").One().Worktree().Root()

	pool := NewRepositoryPool()
	max := 64

	for i := 0; i < max; i++ {
		pool.Add(strconv.Itoa(i), path)
	}

	testRepoIter(max, require, &pool)

	// Test multiple iterators at the same time

	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			testRepoIter(max, require, &pool)
			wg.Done()
		}()
	}

	wg.Wait()
}

func TestRepositoryPoolAddDir(t *testing.T) {
	require := require.New(t)

	tmpDir, err := ioutil.TempDir("", "gitquery-test")
	require.Nil(err)

	max := 64

	for i := 0; i < max; i++ {
		orig := fixtures.Basic().ByTag("worktree").One().Worktree().Root()
		p := filepath.Join(tmpDir, strconv.Itoa(i))

		err := os.Rename(orig, p)
		require.Nil(err)
	}

	pool := NewRepositoryPool()
	err = pool.AddDir(tmpDir)
	require.Nil(err)

	require.Equal(max, len(pool.repositories))

	arrayID := make([]string, max)
	arrayExpected := make([]string, max)

	for i := 0; i < max; i++ {
		repo, err := pool.GetPos(i)
		require.Nil(err)
		arrayID[i] = repo.ID
		arrayExpected[i] = strconv.Itoa(i)

		iter, err := repo.Repo.CommitObjects()
		require.Nil(err)

		counter := 0
		for {
			commit, err := iter.Next()
			if err == io.EOF {
				break
			}

			require.Nil(err)
			require.NotNil(commit)
			counter++
		}

		require.Equal(9, counter)
	}

	require.ElementsMatch(arrayExpected, arrayID)
}
