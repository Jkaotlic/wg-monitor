package alerts

import "sync"

// Очередь доставок по проверке -- общая для всех, кто шлёт по паре (роутер,
// проверка) сообщения, зависящие друг от друга: фоновая доставка тревог из
// /v1/report (карточка, «восстановилось») и обход напоминаний, который
// доставляет недошедшую карточку сам (DeliverHard). Без общей очереди
// «восстановилось» могло уйти раньше карточки, которая ещё висит на
// Telegram, или стереть alert_messages тревоги, ушедшей следом.
//
// Пакетная, а не поле Dispatcher: обработчик отчёта и обход напоминаний
// держат диспетчер за разными интерфейсами, а очередь у процесса одна.

type deliveryKey struct {
	userID int64
	check  string
}

var deliveryQueue = struct {
	mu    sync.Mutex
	tails map[deliveryKey]chan struct{}
}{tails: map[deliveryKey]chan struct{}{}}

// EnqueueDelivery ставит доставку в хвост очереди проверки. prev --
// завершение предыдущей доставки (nil, если очередь пуста): ждать его
// вызывающий обязан сам, со своим сроком. release -- доставка закончилась:
// следующая может идти, хвост удаляет запись очереди. Звать release ровно
// один раз, в том числе когда доставку бросили, не дождавшись.
func EnqueueDelivery(userID int64, checkName string) (prev <-chan struct{}, release func()) {
	key := deliveryKey{userID: userID, check: checkName}
	done := make(chan struct{})
	deliveryQueue.mu.Lock()
	if p, ok := deliveryQueue.tails[key]; ok {
		prev = p
	}
	deliveryQueue.tails[key] = done
	deliveryQueue.mu.Unlock()
	return prev, releaser(key, done)
}

// releaser -- release для места done в очереди key: однократный.
func releaser(key deliveryKey, done chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			deliveryQueue.mu.Lock()
			if deliveryQueue.tails[key] == done {
				delete(deliveryQueue.tails, key)
			}
			deliveryQueue.mu.Unlock()
			close(done)
		})
	}
}

// TryEnqueueDelivery -- EnqueueDelivery, только если очередь проверки пуста:
// для обхода напоминаний, которому ждать нельзя (он один на все тревоги).
// ok=false -- впереди доставка, место не занято; повтор на следующем обходе.
func TryEnqueueDelivery(userID int64, checkName string) (release func(), ok bool) {
	key := deliveryKey{userID: userID, check: checkName}
	deliveryQueue.mu.Lock()
	defer deliveryQueue.mu.Unlock()
	if _, busy := deliveryQueue.tails[key]; busy {
		return nil, false
	}
	done := make(chan struct{})
	deliveryQueue.tails[key] = done
	return releaser(key, done), true
}

// DeliveryQueueLen -- сколько очередей доставки не закончено (для тестов).
func DeliveryQueueLen() int {
	deliveryQueue.mu.Lock()
	defer deliveryQueue.mu.Unlock()
	return len(deliveryQueue.tails)
}
