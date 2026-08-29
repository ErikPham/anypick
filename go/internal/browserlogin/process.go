package browserlogin

type childController interface {
	terminate()
	kill()
	close()
}
