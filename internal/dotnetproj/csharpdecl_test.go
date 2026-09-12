package dotnetproj

import (
	"strings"
	"testing"
)

// A file that declares a class next to an enum whose name STARTS with the class's name was read as
// the enum: the check was `strings.Contains(src, "enum "+typeName)`, and "enum BasketState"
// contains "enum Basket". The class then got the enum's members.
//
// Run run-1789245041122 is the case, and it is the worst shape this can fail in — the generator
// told the model that Basket.Add() does not exist and offered it "CheckedOut, Locked, Open"
// instead. Add(BasketLine) is a public method of Basket. A correct test was rejected, its one
// retry spent, and the model was handed a false statement about the type it was testing.
func TestDeclaredMemberNames_classIsNotMistakenForANeighbouringEnum(t *testing.T) {
	const src = `namespace Shop.Core.Models;

public enum BasketState
{
    Open,
    Locked,
    CheckedOut,
}

public class Basket
{
    public string Id { get; }
    public void Add(BasketLine line) { }
    public long TotalMinor() => 0;
    public void Lock() { }
    private bool IsEmpty() => true;
}`
	got := DeclaredMemberNames("Basket", src)
	has := func(name string) bool {
		for _, g := range got {
			if g == name {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"Add", "TotalMinor", "Lock", "Id"} {
		if !has(want) {
			t.Errorf("DeclaredMemberNames(Basket) = %v, missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"Open", "Locked", "CheckedOut"} {
		if has(unwanted) {
			t.Errorf("DeclaredMemberNames(Basket) = %v, contains the enum member %q", got, unwanted)
		}
	}
	// Private members are excluded on purpose: offering one trades CS1061 for CS0122.
	if has("IsEmpty") {
		t.Errorf("DeclaredMemberNames(Basket) = %v, offers a private member", got)
	}
}

// The enum itself still resolves to its own members, and to the right enum's when a file holds more
// than one — the body used to come from whichever enum appeared first.
//
// Enum members are returned in DECLARATION order, not sorted: the order is part of what the source
// says, and an explicit value assignment is dropped rather than carried into the name.
func TestDeclaredMemberNames_enumResolvesToItsOwnMembers(t *testing.T) {
	const src = `namespace Shop;
public enum Colour { Red, Green, Blue }
public enum BasketState { Open, Locked = 3, CheckedOut }
`
	if got := strings.Join(DeclaredMemberNames("BasketState", src), ","); got != "Open,Locked,CheckedOut" {
		t.Errorf("DeclaredMemberNames(BasketState) = %q, want the second enum's members", got)
	}
	if c := strings.Join(DeclaredMemberNames("Colour", src), ","); c != "Red,Green,Blue" {
		t.Errorf("DeclaredMemberNames(Colour) = %q, want the first enum's members", c)
	}
}

// BaseTypeNames must not confuse a type with a longer-named neighbour either.
func TestBaseTypeNames_matchesTheWholeName(t *testing.T) {
	const src = `public class BasketState : IComparable { }
public class Basket { }
`
	if got := BaseTypeNames("Basket", src); len(got) != 0 {
		t.Errorf("BaseTypeNames(Basket) = %v, want none — IComparable belongs to BasketState", got)
	}
	if got := BaseTypeNames("BasketState", src); len(got) != 1 || got[0] != "IComparable" {
		t.Errorf("BaseTypeNames(BasketState) = %v, want [IComparable]", got)
	}
}
