package main

import "fmt"

type Bucket struct{ Tokens int }

// Value receiver: b is a COPY of the caller's bucket. The change vanishes on return.
func (b Bucket) TakeCopy() { b.Tokens-- }

// Pointer receiver: b is the ADDRESS of the caller's bucket. The change sticks.
func (b *Bucket) Take() { b.Tokens-- }

func main() {
	b := Bucket{Tokens: 3}

	b.TakeCopy()
	fmt.Println("after TakeCopy:", b.Tokens) // still 3: a silent bug that go vet doesn't flag

	b.Take()                             // Go quietly turns this into (&b).Take()
	fmt.Println("after Take:", b.Tokens) // 2

	// & takes an address, * follows one.
	p := &b                               // p has type *Bucket: "pointer to a Bucket"
	p.Tokens = 10                         // shorthand for (*p).Tokens = 10
	fmt.Println("via pointer:", b.Tokens) // 10, since p and b are the same bucket

	// Why the limiter stores *Bucket in its map: map values can't be modified in place,
	// so with map[string]Bucket the Take() line below would not even compile.
	buckets := map[string]*Bucket{"1.1.1.1": {Tokens: 3}}
	buckets["1.1.1.1"].Take()
	fmt.Println("in map:", buckets["1.1.1.1"].Tokens) // 2
}
