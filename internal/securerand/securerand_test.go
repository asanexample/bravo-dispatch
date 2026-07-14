package securerand

import "testing"

func TestIntnRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := Intn(7)
		if v < 0 || v >= 7 {
			t.Fatalf("Intn(7) = %d, want [0,7)", v)
		}
	}
}

func TestIntnPanicsOnNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Intn(0) did not panic")
		}
	}()
	Intn(0)
}

func TestIntNonNegative(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if Int() < 0 {
			t.Fatal("Int() returned a negative value")
		}
	}
}
