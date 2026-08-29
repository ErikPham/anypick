package ownership

type detachedProcess interface {
	terminate() error
	close() error
}
