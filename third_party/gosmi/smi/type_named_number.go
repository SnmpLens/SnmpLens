package smi

import (
	"unsafe"

	"github.com/sleepinggenius2/gosmi/smi/internal"
	"github.com/sleepinggenius2/gosmi/types"
)

// SmiNamedNumber *smiGetFirstNamedNumber(SmiType *smiTypePtr)
func GetFirstNamedNumber(smiTypePtr *types.SmiType) *types.SmiNamedNumber {
	if smiTypePtr == nil {
		return nil
	}
	typePtr := (*internal.Type)(unsafe.Pointer(smiTypePtr))
	if typePtr.List == nil || typePtr.List.Ptr == nil || (typePtr.BaseType != types.BaseTypeEnum && typePtr.BaseType != types.BaseTypeBits) {
		return nil
	}
	// SnmpLens (third_party/README.md): an enumerated type restricted by a
	// value, SYNTAX InetAddressType(1), holds a range where a named number is
	// expected, and the unchecked assertion here was a panic.
	nn, ok := typePtr.List.Ptr.(*internal.NamedNumber)
	if !ok {
		return nil
	}
	return &nn.SmiNamedNumber
}

// SmiNamedNumber *smiGetNextNamedNumber(SmiNamedNumber *smiNamedNumberPtr)
func GetNextNamedNumber(smiNamedNumberPtr *types.SmiNamedNumber) *types.SmiNamedNumber {
	if smiNamedNumberPtr == nil {
		return nil
	}
	nnPtr := (*internal.NamedNumber)(unsafe.Pointer(smiNamedNumberPtr))
	if nnPtr.Type == nil || (nnPtr.Type.BaseType != types.BaseTypeEnum && nnPtr.Type.BaseType != types.BaseTypeBits) {
		return nil
	}
	if nnPtr.List == nil || nnPtr.List.Next == nil || nnPtr.List.Next.Ptr == nil {
		return nil
	}
	nn, ok := nnPtr.List.Next.Ptr.(*internal.NamedNumber)
	if !ok {
		return nil
	}
	return &nn.SmiNamedNumber
}
