package webhook

type UnboundedQueue[T any] struct {
	in  chan T
	out chan T
}

func NewUnboundedQueue[T any]() *UnboundedQueue[T] {
	q := &UnboundedQueue[T]{
		in:  make(chan T),
		out: make(chan T),
	}
	go q.run()
	return q
}

func (q *UnboundedQueue[T]) Enqueue(value T) {
	q.in <- value
}

func (q *UnboundedQueue[T]) Out() <-chan T {
	return q.out
}

func (q *UnboundedQueue[T]) run() {
	queue := make([]T, 0)
	for {
		var (
			next T
			out  chan T
		)
		if len(queue) > 0 {
			next = queue[0]
			out = q.out
		}
		select {
		case value, ok := <-q.in:
			if !ok {
				close(q.out)
				return
			}
			queue = append(queue, value)
		case out <- next:
			queue = queue[1:]
			if len(queue) == 0 {
				queue = nil
			}
		}
	}
}
