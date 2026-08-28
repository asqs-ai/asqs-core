package apisurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepoJava lays out petclinic-shaped sources: the hierarchy, annotations, and member sets
// mirror what run api-a4678e03289277effe4a01043c1bc3ca's compiler output proved (Owner has no
// setPets, Vets has getVetList and no setVets/getVets).
func writeRepoJava(t *testing.T, repo, rel, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func petclinicShapedRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/model/BaseEntity.java", `package org.springframework.samples.petclinic.model;
import java.io.Serializable;
public class BaseEntity implements Serializable {
	private Integer id;
	public Integer getId() { return id; }
	public void setId(Integer id) { this.id = id; }
	public boolean isNew() { return this.id == null; }
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/model/NamedEntity.java", `package org.springframework.samples.petclinic.model;
public class NamedEntity extends BaseEntity {
	private String name;
	public String getName() { return this.name; }
	public void setName(String name) { this.name = name; }
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/model/Person.java", `package org.springframework.samples.petclinic.model;
public class Person extends BaseEntity {
	private String firstName;
	public String getFirstName() { return this.firstName; }
	public void setFirstName(String firstName) { this.firstName = firstName; }
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/owner/Owner.java", `package org.springframework.samples.petclinic.owner;
import org.springframework.samples.petclinic.model.Person;
public class Owner extends Person {
	private final java.util.List<Pet> pets = new java.util.ArrayList<>();
	public java.util.List<Pet> getPets() { return this.pets; }
	public void addPet(Pet pet) { if (pet.isNew()) { getPets().add(pet); } }
	public Pet getPet(String name) { return null; }
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/owner/Pet.java", `package org.springframework.samples.petclinic.owner;
import org.springframework.samples.petclinic.model.NamedEntity;
public class Pet extends NamedEntity {
	public void setOwner(Owner owner) {}
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/vet/Vets.java", `package org.springframework.samples.petclinic.vet;
import java.util.ArrayList;
import java.util.List;
public class Vets {
	private List<Vet> vets;
	public List<Vet> getVetList() {
		if (vets == null) { vets = new ArrayList<>(); }
		return vets;
	}
}
`)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/vet/Vet.java", `package org.springframework.samples.petclinic.vet;
import org.springframework.samples.petclinic.model.Person;
public class Vet extends Person {
}
`)
	return repo
}

// The exact shape the run generated: owner.setPets(...) does not exist; the repair is addPet.
func TestRepoInventedMember_ownerSetPets(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.owner;
import java.util.List;
import org.junit.jupiter.api.Test;
class OwnerTests {
	@Test
	void t() {
		Owner owner = new Owner();
		owner.setFirstName("George");
		owner.setPets(List.of(new Pet()));
	}
}
`
	got := RepoInventedMemberReason(repo, test)
	if got == "" {
		t.Fatal("owner.setPets must be reported: Owner's whole repo hierarchy declares no such member")
	}
	if want := "setPets"; !containsAll(got, want, "Owner") {
		t.Errorf("reason = %q, want it to name %s on Owner", got, want)
	}
}

// vets.setVets / vets.getVets from the same run; Vets declares only getVetList.
func TestRepoInventedMember_vetsSetVets(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.vet;
import org.junit.jupiter.api.Test;
class VetsTests {
	@Test
	void t() {
		Vets vets = new Vets();
		vets.setVets(null);
	}
}
`
	if got := RepoInventedMemberReason(repo, test); got == "" || !containsAll(got, "setVets", "Vets") {
		t.Errorf("reason = %q, want setVets on Vets reported", got)
	}
}

// Inherited and Object members must never be reported: getFirstName lives two classes up,
// isNew three, toString on Object. This is the false-rejection direction and it must stay shut.
func TestRepoInventedMember_inheritedAndObjectMembersAllowed(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.owner;
import org.junit.jupiter.api.Test;
class OwnerTests {
	@Test
	void t() {
		Owner owner = new Owner();
		owner.setFirstName("George");
		owner.getFirstName();
		owner.isNew();
		owner.toString();
		owner.addPet(new Pet());
		owner.getPet("x").getName();
		var vets = new Vets();
	}
}
`
	// Note: Vets here is not resolvable from this package and carries no import — silence, not
	// a rejection.
	if got := RepoInventedMemberReason(repo, test); got != "" {
		t.Errorf("false rejection: %q", got)
	}
}

// A hierarchy edge that leaves the repo makes the type unprovable: members could live on the
// classpath parent.
func TestRepoInventedMember_classpathParentSilences(t *testing.T) {
	repo := petclinicShapedRepo(t)
	writeRepoJava(t, repo, "src/main/java/org/springframework/samples/petclinic/owner/OwnerRepository.java", `package org.springframework.samples.petclinic.owner;
import org.springframework.data.repository.Repository;
public interface OwnerRepository extends Repository {
	Owner findById(Integer id);
}
`)
	test := `package org.springframework.samples.petclinic.owner;
class T {
	void t(OwnerRepository repo) {
		OwnerRepository r = repo;
		r.findAll();
	}
}
`
	if got := RepoInventedMemberReason(repo, test); got != "" {
		t.Errorf("a chain that leaves the repo must be silent, got %q", got)
	}
}

// Lombok sources synthesize members the text never declares; the check must stand down.
func TestRepoInventedMember_lombokSilences(t *testing.T) {
	repo := t.TempDir()
	writeRepoJava(t, repo, "src/main/java/com/x/Order.java", `package com.x;
import lombok.Data;
@Data
public class Order {
	private String id;
}
`)
	test := "package com.x;\nclass T { void t() { Order o = new Order(); o.getId(); o.setId(\"1\"); } }\n"
	if got := RepoInventedMemberReason(repo, test); got != "" {
		t.Errorf("Lombok getters are invisible to the source scan; must be silent, got %q", got)
	}
}

// Records answer through their components; enums through their built-ins.
func TestRepoInventedMember_recordsAndEnums(t *testing.T) {
	repo := t.TempDir()
	writeRepoJava(t, repo, "src/main/java/com/x/Point.java", "package com.x;\npublic record Point(int x, int y) {}\n")
	writeRepoJava(t, repo, "src/main/java/com/x/Color.java", "package com.x;\npublic enum Color { RED, GREEN }\n")
	ok := `package com.x;
class T { void t() { Point p = new Point(1, 2); p.x(); Color c = Color.RED; c.name(); } }
`
	if got := RepoInventedMemberReason(repo, ok); got != "" {
		t.Errorf("record components and enum built-ins are real members, got %q", got)
	}
	bad := "package com.x;\nclass T { void t() { Point p = new Point(1, 2); p.z(); } }\n"
	if got := RepoInventedMemberReason(repo, bad); got == "" {
		t.Error("a component the record does not declare must be reported")
	}
}

// A member mentioned only inside a string or comment must not count as a call, and a call named
// in a comment must not become an allowed member.
func TestRepoInventedMember_stringsAndCommentsStripped(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.owner;
class T {
	void t() {
		Owner owner = new Owner();
		String s = "owner.setPets(x)";
		// owner.setPets(y)
		owner.addPet(new Pet());
	}
}
`
	if got := RepoInventedMemberReason(repo, test); got != "" {
		t.Errorf("literal/comment mentions are not calls, got %q", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// Two invented members on two different repo types, in one file. Reporting the first left the
// second to be discovered a compile round later, against a regeneration budget of one.
func TestRepoInventedMember_reportsEveryViolation(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.vet;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.springframework.samples.petclinic.owner.Owner;
import org.springframework.samples.petclinic.owner.Pet;
class MixedTests {
	@Test
	void t() {
		Vets vets = new Vets();
		vets.setVets(null);
		Owner owner = new Owner();
		owner.setPets(List.of(new Pet()));
	}
}
`
	got := RepoInventedMemberReason(repo, test)
	if !containsAll(got, "setVets", "Vets") {
		t.Errorf("reason = %q, want setVets on Vets reported", got)
	}
	if !containsAll(got, "setPets", "Owner") {
		t.Errorf("reason = %q, want setPets on Owner reported in the SAME pass", got)
	}
}

// The locals map was ranged over directly, so "the first violation" was whichever one Go's
// randomised map iteration reached first: identical content could yield different findings.
func TestRepoInventedMember_isDeterministic(t *testing.T) {
	repo := petclinicShapedRepo(t)
	test := `package org.springframework.samples.petclinic.vet;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.springframework.samples.petclinic.owner.Owner;
import org.springframework.samples.petclinic.owner.Pet;
class MixedTests {
	@Test
	void t() {
		Vets vets = new Vets();
		vets.setVets(null);
		Owner owner = new Owner();
		owner.setPets(List.of(new Pet()));
	}
}
`
	first := RepoInventedMemberReason(repo, test)
	for i := 0; i < 25; i++ {
		if got := RepoInventedMemberReason(repo, test); got != first {
			t.Fatalf("run %d differed:\n first = %q\n got   = %q", i, first, got)
		}
	}
}
