package main

import (
	"fmt"
	"net/http"
	"time"
)

func main() {
	go func() {
		err := http.ListenAndServe("127.0.0.1:8000", nil)
		fmt.Println("127.0.0.1:8000 ->", err)
	}()
	time.Sleep(100 * time.Millisecond)

	err := http.ListenAndServe(":8000", nil)
	fmt.Println("[::]:8000 ->", err)
}
