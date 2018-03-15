package gitquery

import (
	"io"
	"io/ioutil"
	"path/filepath"
	"runtime"
	"sync"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-mysql-server.v0/sql"
)

// Repository struct holds an initialized repository and its ID
type Repository struct {
	ID   string
	Repo *git.Repository
}

// NewRepository creates and initializes a new Repository structure
func NewRepository(id string, repo *git.Repository) Repository {
	return Repository{
		ID:   id,
		Repo: repo,
	}
}

// NewRepositoryFromPath creates and initializes a new Repository structure
// and initializes a go-git repository
func NewRepositoryFromPath(id, path string) (Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return Repository{}, err
	}

	return NewRepository(id, repo), nil
}

// RepositoryPool holds a pool git repository paths and
// functionality to open and iterate them.
type RepositoryPool struct {
	repositories map[string]string
	idOrder      []string
}

// NewRepositoryPool initializes a new RepositoryPool
func NewRepositoryPool() RepositoryPool {
	return RepositoryPool{
		repositories: make(map[string]string),
	}
}

// Add inserts a new repository in the pool
func (p *RepositoryPool) Add(id, path string) {
	_, ok := p.repositories[id]
	if !ok {
		p.idOrder = append(p.idOrder, id)
	}

	p.repositories[id] = path
}

// AddGit checks if a git repository can be opened and adds it to the pool. It
// also sets its path as ID.
func (p *RepositoryPool) AddGit(path string) (string, error) {
	_, err := git.PlainOpen(path)
	if err != nil {
		return "", err
	}

	id := filepath.Base(path)
	p.Add(id, path)

	return id, nil
}

// AddDir adds all direct subdirectories from path as repos
func (p *RepositoryPool) AddDir(path string) error {
	dirs, err := ioutil.ReadDir(path)
	if err != nil {
		return err
	}

	for _, f := range dirs {
		if f.IsDir() {
			name := filepath.Join(path, f.Name())
			// TODO: log that the repo could not be opened
			p.AddGit(name)
		}
	}

	return nil
}

// GetPos retrieves a repository at a given position. If the position is
// out of bounds it returns io.EOF
func (p *RepositoryPool) GetPos(pos int) (*Repository, error) {
	if pos >= len(p.repositories) {
		return nil, io.EOF
	}

	id := p.idOrder[pos]
	if id == "" {
		return nil, io.EOF
	}

	path := p.repositories[id]
	repo, err := NewRepositoryFromPath(id, path)
	if err != nil {
		return nil, err
	}

	return &repo, nil
}

// RepoIter creates a new Repository iterator
func (p *RepositoryPool) RepoIter() (*RepositoryIter, error) {
	iter := &RepositoryIter{
		pos:  0,
		pool: p,
	}

	return iter, nil
}

// RepositoryIter iterates over all repositories in the pool
type RepositoryIter struct {
	pos  int
	pool *RepositoryPool
}

// Next retrieves the next Repository. It returns io.EOF as error
// when there are no more Repositories to retrieve.
func (i *RepositoryIter) Next() (*Repository, error) {
	r, err := i.pool.GetPos(i.pos)
	if err != nil {
		return nil, err
	}

	i.pos++

	return r, nil
}

// Close finished iterator. It's no-op.
func (i *RepositoryIter) Close() error {
	return nil
}

// RowRepoIter is the interface needed by each iterator
// implementation
type RowRepoIter interface {
	NewIterator(*Repository) (RowRepoIter, error)
	Next() (sql.Row, error)
	Close() error
}

// RowRepoIter is used as the base to iterate over all the repositories
// in the pool
type rowRepoIter struct {
	repositoryIter *RepositoryIter
	iter           RowRepoIter

	wg    sync.WaitGroup
	done  chan bool
	err   chan error
	repos chan *Repository
	rows  chan sql.Row
}

// NewRowRepoIter initializes a new repository iterator.
//
// * pool: is a RepositoryPool we want to iterate
// * iter: specific RowRepoIter interface
//     * NewIterator: called when a new repository is about to be iterated,
//         returns a new RowRepoIter
//     * Next: called for each row
//     * Close: called when a repository finished iterating
func NewRowRepoIter(
	pool *RepositoryPool,
	iter RowRepoIter,
) (*rowRepoIter, error) {
	rIter, err := pool.RepoIter()
	if err != nil {
		return nil, err
	}

	repoIter := rowRepoIter{
		repositoryIter: rIter,
		iter:           iter,
		done:           make(chan bool),
		err:            make(chan error),
		repos:          make(chan *Repository),
		rows:           make(chan sql.Row),
	}

	go repoIter.fillRepoChannel()

	wNum := runtime.NumCPU()

	for i := 0; i < wNum; i++ {
		repoIter.wg.Add(1)

		go repoIter.rowReader(i)
	}

	go func() {
		repoIter.wg.Wait()
		close(repoIter.rows)
	}()

	return &repoIter, nil
}

func (i *rowRepoIter) fillRepoChannel() {
	for {
		select {
		case <-i.done:
			return

		default:
			repo, err := i.repositoryIter.Next()

			switch err {
			case nil:
				i.repos <- repo
				continue

			case io.EOF:
				close(i.repos)
				i.err <- io.EOF
				return

			default:
				close(i.done)
				close(i.repos)
				i.err <- err
				return
			}
		}
	}
}

func (i *rowRepoIter) rowReader(num int) {
	defer i.wg.Done()

	for repo := range i.repos {
		iter, _ := i.iter.NewIterator(repo)

	loop:
		for {
			select {
			case <-i.done:
				iter.Close()
				return

			default:
				row, err := iter.Next()
				switch err {
				case nil:
					i.rows <- row

				case io.EOF:
					iter.Close()
					break loop

				default:
					iter.Close()
					i.err <- err
					close(i.done)
					return
				}
			}
		}
	}
}

// Next gets the next row
func (i *rowRepoIter) Next() (sql.Row, error) {
	row, ok := <-i.rows
	if !ok {
		return nil, <-i.err
	}

	return row, nil
}

// Close called to close the iterator
func (i *rowRepoIter) Close() error {
	return i.iter.Close()
}
