package super

type Supervisee interface {
	Start() error
}

func Vise(s Supervisee, name string) Supervisee {
	panic("not implemented")
}
