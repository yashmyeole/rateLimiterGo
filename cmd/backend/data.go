package main

// Canned data. Every instance serves the same data, like replicas of one service.
//
// encoding/json only sees exported (capitalized) fields; the struct tags set the
// JSON key names.

type user struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type order struct {
	ID         int    `json:"id"`
	UserID     int    `json:"user_id"`
	Item       string `json:"item"`
	TotalCents int    `json:"total_cents"` // money as integer cents, not float
}

type quote struct {
	Symbol    string  `json:"symbol"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
}

var users = []user{
	{ID: 1, Name: "Asha Rao", Email: "asha@example.com"},
	{ID: 2, Name: "Ben Carter", Email: "ben@example.com"},
	{ID: 3, Name: "Chen Wei", Email: "chen@example.com"},
}

var orders = []order{
	{ID: 101, UserID: 1, Item: "mechanical keyboard", TotalCents: 8900},
	{ID: 102, UserID: 3, Item: "usb-c hub", TotalCents: 3450},
	{ID: 103, UserID: 2, Item: "monitor arm", TotalCents: 12999},
}

var quotes = []quote{
	{Symbol: "ACME", Price: 142.37, ChangePct: 1.2},
	{Symbol: "GLBX", Price: 58.10, ChangePct: -0.4},
	{Symbol: "INIT", Price: 311.05, ChangePct: 0},
}
