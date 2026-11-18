package main

import (
	"os"
	"testing"
)

func TestObserverCanVerifyItsRealParent(t *testing.T) {
	alive, closeParent, err := observeParent(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	defer closeParent()
	if !alive() {
		t.Fatal("真实父进程被错误判为停止")
	}
}
