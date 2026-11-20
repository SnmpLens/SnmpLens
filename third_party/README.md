# Third-party code kept in this repository

## gosmi

`gosmi/` is [github.com/sleepinggenius2/gosmi](https://github.com/sleepinggenius2/gosmi) v0.4.4,
MIT-licensed (`gosmi/LICENSE`), the MIB engine `pkg/mib` stands on. `go.mod` points the module at
this copy with a `replace`. Upstream has had no commit since February 2022, so a fix that has to
ship cannot wait for a release there.

It is v0.4.4 byte for byte except for what is listed below. Compare it with
`go mod download -json github.com/sleepinggenius2/gosmi@v0.4.4` and `diff -r` before changing
anything; anything not listed here is a mistake.

### `smi/internal/object.go`, `linkObject`: an arc already in the tree

An OID value written with more than one arc after its parent — `{ iso std(0) 62439 100 }`,
`{ iso org(3) dod(6) internet(1) private(4) enterprises(1) 99991 }` — creates a node for every arc
between the first and the last. When a node was already at that sub-identifier, created by another
module or by an earlier definition of the same one, `NodeChildMap.Add` kept the existing node and
dropped the new one, while `linkObject` carried on from the dropped one: every arc after it hung
from a node outside the tree, with no OID, and the module's objects were absent from it. Two
modules registered under `iso std(0) 62439` were enough for the second one's objects to vanish
(issue #200). The fix carries on from the node `Add` kept: one line.

`pkg/mib`'s `TestALongOidCrossingANodeInTheTreeStaysInIt` fails against unpatched v0.4.4.

### `smi/type_named_number.go`, `smi/type_range.go`: a list of the other kind

A type's list holds named numbers or ranges, and the four accessors asserted which without checking.
An enumerated type restricted by a value — `SYNTAX InetAddressType(1)`, as an Alcatel-Lucent MIB
writes it — holds a range where the named numbers are looked for, and reading its labels was a panic
that failed the whole load, every MIB with it. A list of the other kind now reads as none.

`pkg/mib`'s `TestAnEnumerationRestrictedByAValueLoads` panics against unpatched v0.4.4.

### `smi/internal/module.go`, `BuildModule`: a type it cannot place

The module's types were read in order with a `break` at the first one gosmi could not place: one whose
SYNTAX names a type nothing defines, or a textual convention whose SYNTAX is another one, which RFC 2579
forbids and vendor MIBs are full of. Every type after it was undefined, the tables' SEQUENCEs among
them, so their columns were read as scalars, objects of the later types had none, and SET had no wire
type. On a corpus of 196 vendor MIBs that was 12 567 columns. A type gosmi cannot place is now skipped
alone, and a textual convention of another one takes its parent's base type, and its parent's labels
and ranges when it declares none of its own.

`pkg/mib`'s `TestTypesAfterOneGosmiCannotPlaceAreStillRead` fails against unpatched v0.4.4.
