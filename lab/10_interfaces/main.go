package main

import (
	"fmt"
	"math"
)

// An interface is a list of methods. Any type that has them satisfies it automatically;
// there's no "implements" keyword. Phase 10 uses this to swap rate-limit algorithms by config.
type Shape interface {
	Area() float64
}

type Rect struct{ W, H float64 }

// Value receivers are fine here: Area only reads the shape, never changes it.
func (r Rect) Area() float64 { return r.W * r.H }

type Circle struct{ R float64 }

func (c Circle) Area() float64 { return math.Pi * c.R * c.R }

// totalArea only knows about Shape, so it never changes when a new shape appears.
func totalArea(shapes []Shape) float64 {
	sum := 0.0
	for _, s := range shapes {
		sum += s.Area()
	}
	return sum
}

func main() {
	// Adding a Triangle (any type with an Area method) to this slice needs no change
	// to totalArea or the loop below. That's the point of the interface.
	shapes := []Shape{Rect{W: 2, H: 3}, Circle{R: 1}}

	for _, s := range shapes {
		fmt.Printf("%T area = %.2f\n", s, s.Area()) // %T prints the concrete type inside
	}
	fmt.Printf("total = %.2f\n", totalArea(shapes))
}
