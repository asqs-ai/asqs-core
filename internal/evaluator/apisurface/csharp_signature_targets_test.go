package apisurface

import "testing"

// Carried from CS18: the C# arm waited on the indexer emitting a `signature`, which it now does.
//
// Java maps each simple name in a signature through the file's IMPORT block, because an import
// names a TYPE. C# usings name NAMESPACES, so the same mapping does not exist — a name has to be
// paired with each imported namespace and only counts when exactly one pairing is plausible. What
// survives that is narrower than Java's and deliberately so: the alternative is telling the
// generator to import a type that does not exist.
func TestSignatureTargets_csharp(t *testing.T) {
	const source = `using System;
using System.Collections.Generic;
using Microsoft.EntityFrameworkCore;
using Shop.Core.Models;

namespace Shop.Core.Services;

public class OrderService
{
    public Task<Order> CreateAsync(Order order, DbContextOptions options) => throw new NotImplementedException();
}`
	cases := []struct {
		name      string
		signature string
		want      []string
		absent    []string
	}{
		{
			name:      "a type from an imported namespace",
			signature: "public Task<Order> CreateAsync(Order order, DbContextOptions options)",
			want:      []string{"Microsoft.EntityFrameworkCore.DbContextOptions"},
			// Order is declared by this repository; the fixer already ships repo source, and
			// dropRepoOwnedTargets removes it at the call site anyway.
			absent: []string{"Task", "string", "int"},
		},
		{
			name:      "a signature naming only well-known containers",
			signature: "public List<string> Names(int count)",
			want:      nil,
		},
		{
			name:      "an empty signature",
			signature: "",
			want:      nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SignatureTargets("csharp", tc.signature, source)
			names := map[string]bool{}
			for _, g := range got {
				names[g.Name] = true
			}
			for _, w := range tc.want {
				if !names[w] {
					t.Errorf("SignatureTargets = %+v, missing %s", got, w)
				}
			}
			for _, a := range tc.absent {
				for n := range names {
					if n == a || len(n) > len(a) && n[len(n)-len(a):] == a {
						t.Errorf("SignatureTargets = %+v, should not include %s", got, a)
					}
				}
			}
			if len(tc.want) == 0 && len(got) != 0 {
				t.Errorf("SignatureTargets = %+v, want none", got)
			}
		})
	}
}

// The Java arm must keep working: a regression here trades a fixed C# gap for a broken Java one.
func TestSignatureTargets_javaStillResolves(t *testing.T) {
	const source = `package shop;

import org.springframework.http.ResponseEntity;

public class Controller {}`
	got := SignatureTargets("java", "public ResponseEntity<String> get(String id)", source)
	found := false
	for _, g := range got {
		if g.Name == "org.springframework.http.ResponseEntity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SignatureTargets = %+v, want org.springframework.http.ResponseEntity", got)
	}
}
