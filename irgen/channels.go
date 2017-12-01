//===- channels.go - IR generation for channels ---------------------------===//
//
//                     The LLVM Compiler Infrastructure
//
// This file is distributed under the University of Illinois Open Source
// License. See LICENSE.TXT for details.
//
//===----------------------------------------------------------------------===//
//
// This file implements IR generation for channels.
//
//===----------------------------------------------------------------------===//

package irgen

import (
	"fmt"
	"llvm.org/llgo/third_party/gotools/go/ssa"
	"llvm.org/llgo/third_party/gotools/go/types"
	"llvm.org/llvm/bindings/go/llvm"
)

// makeChan implements make(chantype[, size])
func (fr *frame) makeChan(chantyp types.Type, size *govalue) *govalue {
	// TODO(pcc): call __go_new_channel_big here if needed
	// NOTE(growly): What is all channels are Uint64 now?
	// TypeMap.ToRuntime casts this to an Unsafe Pointer? i8*
	size = fr.convert(size, types.Typ[types.Uintptr])
	fifoWidth := llvm.ConstInt(llvm.Int8Type(), uint64(fr.types.llvmTypeMap.Sizeof(chantyp) * 8), false)
	ch := fr.runtime.newChannelFifo.call(fr, fifoWidth, size.value)[0]
	// TODO(growly): Am I doing it right?
	fmt.Println("arya: emitting ssa for make chan of chantyp", chantyp , "width", fifoWidth, "size", size)
	return newValue(ch, chantyp)
}

func (fr *frame) makeChanInternal(chantyp types.Type, size *govalue) *govalue {
	// TODO(pcc): call __go_new_channel_big here if needed
	dyntyp := fr.types.ToRuntime(chantyp)
	size = fr.convert(size, types.Typ[types.Uintptr])
	ch := fr.runtime.newChannel.call(fr, dyntyp, size.value)[0]
	return newValue(ch, chantyp)
}

// chanSend implements ch<- x
func (fr *frame) chanSend(ch *govalue, elem *govalue) {
	elemtyp := ch.Type().Underlying().(*types.Chan).Elem()
	elem = fr.convert(elem, elemtyp)
	elemptr := fr.allocaBuilder.CreateAlloca(elem.value.Type(), "")
	fr.builder.CreateStore(elem.value, elemptr)
	elemptr = fr.builder.CreateBitCast(elemptr, llvm.PointerType(llvm.Int8Type(), 0), "")
	//chantyp := fr.types.ToRuntime(ch.Type())
	// fr.runtime.sendBig.call(fr, chantyp, ch.value, elemptr)
	// TODO(growly): We need to deref elemptr... and convert it to a
	// Uint64, which is what the fifo interface takes.
	fr.runtime.sendBigFifo.call(fr, ch.value, elemptr)
}

func (fr *frame) chanSendInternal(ch *govalue, elem *govalue) {
	elemtyp := ch.Type().Underlying().(*types.Chan).Elem()
	elem = fr.convert(elem, elemtyp)
	elemptr := fr.allocaBuilder.CreateAlloca(elem.value.Type(), "")
	fr.builder.CreateStore(elem.value, elemptr)
	elemptr = fr.builder.CreateBitCast(elemptr, llvm.PointerType(llvm.Int8Type(), 0), "")
	chantyp := fr.types.ToRuntime(ch.Type())
	fr.runtime.sendBig.call(fr, chantyp, ch.value, elemptr)
}


func (fr *frame) chanRecvFifo(ch *govalue, commaOk bool) (x, ok *govalue) {
	elemtyp := ch.Type().Underlying().(*types.Chan).Elem()

	ptr := fr.allocaBuilder.CreateAlloca(fr.types.ToLLVM(elemtyp), "")
	// ptri8 := fr.builder.CreateBitCast(ptr, llvm.PointerType(llvm.Int8Type(), 0), "")
	// chantyp := fr.types.ToRuntime(ch.Type())

	if commaOk {
		// okval := fr.runtime.chanrecv2.call(fr, chantyp, ch.value, ptri8)[0]
		// ok = newValue(okval, types.Typ[types.Bool])
		panic("chanRecvFifo can't support the comma'd form of channel receive yet")
	} else {
		// TODO(growly): 'ch' is a handle to the channel itself, look in ch.value. FIFOs always return a 'long long'.
		//recvval := fr.runtime.receive.call(fr, ch.value)[0]
		//recvval = fr.builder.CreateBitCast(recvval, fr.types.ToLLVM(elemtyp), "")
		//fmt.Println("recvval is", recvval)
		// Store to load again?! What are you up to?
		//fr.builder.CreateStore(recvval, ptr)

		recvval := newValue(fr.runtime.receiveFifo.call(fr, ch.value)[0], types.Typ[types.Int64])
		x = fr.convert(recvval, elemtyp)
		fr.builder.CreateStore(x.value, ptr)
	}
	x = newValue(fr.builder.CreateLoad(ptr, ""), elemtyp)
	return
}

// Use the golib channel implementation.
// TODO(growly): If this is to co-exist with the LegUp FIFO channels, all of
// the standard internal channel functions should remain included. And we need
// a trivial test for whether conversion can work, instead of panicking in the
// conversion.
func (fr *frame) chanRecvInternal(ch *govalue, commaOk bool) (x, ok *govalue) {
	elemtyp := ch.Type().Underlying().(*types.Chan).Elem()

	ptr := fr.allocaBuilder.CreateAlloca(fr.types.ToLLVM(elemtyp), "")
	ptri8 := fr.builder.CreateBitCast(ptr, llvm.PointerType(llvm.Int8Type(), 0), "")
	chantyp := fr.types.ToRuntime(ch.Type())

	if commaOk {
		okval := fr.runtime.chanrecv2.call(fr, chantyp, ch.value, ptri8)[0]
		ok = newValue(okval, types.Typ[types.Bool])
	} else {
		fr.runtime.receive.call(fr, chantyp, ch.value, ptri8)
	}
	x = newValue(fr.builder.CreateLoad(ptr, ""), elemtyp)
	return
}

// chanRecv implements x[, ok] = <-ch
func (fr *frame) chanRecv(ch *govalue, commaOk bool) (x, ok *govalue) {
	defer func() {
		if r:= recover(); r != nil {
			fmt.Println("Panic in chanRecv, will attempt internal method:", r)
			x, ok = fr.chanRecvInternal(ch, commaOk)
			return
		}
	}()
	x, ok = fr.chanRecvFifo(ch, commaOk)
	return
}

// chanClose implements close(ch)
func (fr *frame) chanClose(ch *govalue) {
	fr.runtime.builtinClose.call(fr, ch.value)
}

func (fr *frame) chanSelect(sel *ssa.Select) (index, recvOk *govalue, recvElems []*govalue) {
	n := uint64(len(sel.States))
	if !sel.Blocking {
		// non-blocking means there's a default case
		n++
	}
	size := llvm.ConstInt(llvm.Int32Type(), n, false)
	selectp := fr.runtime.newSelect.call(fr, size)[0]

	// Allocate stack for the values to send and receive.
	ptrs := make([]llvm.Value, len(sel.States))
	for i, state := range sel.States {
		chantyp := state.Chan.Type().Underlying().(*types.Chan)
		elemtyp := fr.types.ToLLVM(chantyp.Elem())
		if state.Dir == types.SendOnly {
			ptrs[i] = fr.allocaBuilder.CreateAlloca(elemtyp, "")
			fr.builder.CreateStore(fr.llvmvalue(state.Send), ptrs[i])
		} else {
			// Only allocate stack space if the received value is used.
			used := chanSelectStateUsed(sel, len(recvElems))
			if used {
				ptrs[i] = fr.allocaBuilder.CreateAlloca(elemtyp, "")
			} else {
				ptrs[i] = llvm.ConstNull(llvm.PointerType(llvm.Int8Type(), 0))
			}
			recvElems = append(recvElems, newValue(ptrs[i], chantyp.Elem()))
		}
	}

	// Create select{send,recv2} calls.
	var receivedp llvm.Value
	if len(recvElems) > 0 {
		receivedp = fr.allocaBuilder.CreateAlloca(fr.types.ToLLVM(types.Typ[types.Bool]), "")
	}
	if !sel.Blocking {
		// If the default case is chosen, the index must be -1.
		fr.runtime.selectdefault.call(fr, selectp, llvm.ConstAllOnes(llvm.Int32Type()))
	}
	for i, state := range sel.States {
		ch := fr.llvmvalue(state.Chan)
		index := llvm.ConstInt(llvm.Int32Type(), uint64(i), false)
		if state.Dir == types.SendOnly {
			fr.runtime.selectsend.call(fr, selectp, ch, ptrs[i], index)
		} else {
			fr.runtime.selectrecv2.call(fr, selectp, ch, ptrs[i], receivedp, index)
		}
	}

	// Fire off the select.
	index = newValue(fr.runtime.selectgo.call(fr, selectp)[0], types.Typ[types.Int])
	if len(recvElems) > 0 {
		recvOk = newValue(fr.builder.CreateLoad(receivedp, ""), types.Typ[types.Bool])
		for _, recvElem := range recvElems {
			recvElem.value = fr.builder.CreateLoad(recvElem.value, "")
		}
	}
	return index, recvOk, recvElems
}

func chanSelectStateUsed(sel *ssa.Select, recvIndex int) bool {
	for _, instr := range *sel.Referrers() {
		extract, ok := instr.(*ssa.Extract)
		if !ok || extract.Index != (recvIndex+2) {
			continue
		}
		if len(*extract.Referrers()) > 0 {
			return true
		}
	}
	return false
}
