//===- indirect.go - IR generation for thunks -----------------------------===//
//
//                     The LLVM Compiler Infrastructure
//
// This file is distributed under the University of Illinois Open Source
// License. See LICENSE.TXT for details.
//
//===----------------------------------------------------------------------===//
//
// This file implements IR generation for thunks required by the "defer" and
// "go" builtins.
//
//===----------------------------------------------------------------------===//

package irgen

import (
	"fmt"
	"llvm.org/llgo/third_party/gotools/go/ssa"
	"llvm.org/llgo/third_party/gotools/go/types"
	"llvm.org/llvm/bindings/go/llvm"
)

// createThunk creates a thunk from a
// given function and arguments, suitable for use with
// "defer" and "go".
func (fr *frame) createThunk(call ssa.CallInstruction) (thunk llvm.Value, arg llvm.Value) {
	i8ptr := llvm.PointerType(llvm.Int8Type(), 0)
	thunk, arg = fr.createThunkRaw(call)
	thunk = fr.builder.CreateBitCast(thunk, i8ptr, "")
	return
}

// Creates a thunk but doesn't cast the result to a UnsafePointer (uint8*).
func (fr *frame) createThunkRaw(call ssa.CallInstruction) (thunk llvm.Value, arg llvm.Value) {
	seenarg := make(map[ssa.Value]bool)
	var args []ssa.Value
	var argtypes []*types.Var

	packArg := func(arg ssa.Value) {
		switch arg.(type) {
		case *ssa.Builtin, *ssa.Function, *ssa.Const, *ssa.Global:
			// Do nothing: we can generate these in the thunk
		default:
			fmt.Println("arg.Type()", arg.Type().String())
			if !seenarg[arg] {
				seenarg[arg] = true
				args = append(args, arg)
				field := types.NewField(0, nil, "_", arg.Type(), true)
				argtypes = append(argtypes, field)
			}
		}
	}

	packArg(call.Common().Value)
	for _, arg := range call.Common().Args {
		packArg(arg)
	}

	var isRecoverCall bool
	i8ptr := llvm.PointerType(llvm.Int8Type(), 0)
	var structllptr llvm.Type
	if len(args) == 0 {
		if builtin, ok := call.Common().Value.(*ssa.Builtin); ok {
			isRecoverCall = builtin.Name() == "recover"
		}
		if isRecoverCall {
			// When creating a thunk for recover(), we must pass fr.canRecover.
			arg = fr.builder.CreateZExt(fr.canRecover, fr.target.IntPtrType(), "")
			arg = fr.builder.CreateIntToPtr(arg, i8ptr, "")
		} else {
			arg = llvm.ConstPointerNull(i8ptr)
		}
	} else {
		structtype := types.NewStruct(argtypes, nil)
		arg = fr.createTypeMalloc(structtype)
		fmt.Println("arg:", arg.Type().String())
		structllptr = arg.Type()
		for i, ssaarg := range args {
			argptr := fr.builder.CreateStructGEP(arg, i, "")
			llv := fr.llvmvalue(ssaarg)
			fmt.Println("left:", llv.Type().String(), "right:", argptr.Type().String())
			if llv.Type() != arg.Type() {
				fmt.Println("arg types not equal")
			}
			fr.builder.CreateStore(llv, argptr)
		}
		arg = fr.builder.CreateBitCast(arg, i8ptr, "")
	}

	// Create a copy of current closure function 
	// JENNY this is actually like a wrapper function for closure!
	thunkfntype := llvm.FunctionType(llvm.VoidType(), []llvm.Type{i8ptr}, false)
	thunkfn := llvm.AddFunction(fr.module.Module, "", thunkfntype)
	thunkfn.SetLinkage(llvm.InternalLinkage)
	fr.addCommonFunctionAttrs(thunkfn)

	thunkfr := newFrame(fr.unit, thunkfn)
	defer thunkfr.dispose()

	// Place to modify the closure function
	prologuebb := llvm.AddBasicBlock(thunkfn, "prologue")
	thunkfr.builder.SetInsertPointAtEnd(prologuebb)

	if isRecoverCall {
		thunkarg := thunkfn.Param(0)
		thunkarg = thunkfr.builder.CreatePtrToInt(thunkarg, fr.target.IntPtrType(), "")
		thunkfr.canRecover = thunkfr.builder.CreateTrunc(thunkarg, llvm.Int1Type(), "")
	} else if len(args) > 0 {
		thunkarg := thunkfn.Param(0)
		thunkarg = thunkfr.builder.CreateBitCast(thunkarg, structllptr, "")
		for i, ssaarg := range args {
			thunkargptr := thunkfr.builder.CreateStructGEP(thunkarg, i, "")
			thunkarg := thunkfr.builder.CreateLoad(thunkargptr, "")
			thunkfr.env[ssaarg] = newValue(thunkarg, ssaarg.Type())
		}
	}
	_, isDefer := call.(*ssa.Defer)

	entrybb := llvm.AddBasicBlock(thunkfn, "entry")
	br := thunkfr.builder.CreateBr(entrybb)
	thunkfr.allocaBuilder.SetInsertPointBefore(br)

	thunkfr.builder.SetInsertPointAtEnd(entrybb)
	var exitbb llvm.BasicBlock
	if isDefer {
		exitbb = llvm.AddBasicBlock(thunkfn, "exit")
		thunkfr.runtime.setDeferRetaddr.call(thunkfr, llvm.BlockAddress(thunkfn, exitbb))
	}
	if isDefer && isRecoverCall {
		thunkfr.callRecover(true)
	} else {
		thunkfr.callInstruction(call)
	}
	if isDefer {
		thunkfr.builder.CreateBr(exitbb)
		thunkfr.builder.SetInsertPointAtEnd(exitbb)
	}
	thunkfr.builder.CreateRetVoid()
	thunk = thunkfn
	return
}

// We need a wrapper function for pthread_create: it expectes a
// void *(*f)(void*), i.e. it has to invoke a function that returns a value.
// We create that function and always include a terminating pthread_exit to
// make the return value. That wrapper function, created here, is responsible
// for invoking the actual function we want to call.
func (fr *frame) createPthreadWrapper(innerfn llvm.Value) llvm.Value {
	i8ptr := llvm.PointerType(llvm.Int8Type(), 0)
	fntype := llvm.FunctionType(llvm.PointerType(llvm.Int8Type(), 0), []llvm.Type{i8ptr}, false)
	fn := llvm.AddFunction(fr.module.Module, "auto_pthread_wrapper", fntype)
	fn.SetLinkage(llvm.InternalLinkage)
	fr.addCommonFunctionAttrs(fn)

	// TODO(growly): dumb question: do we need a new frame?
	wrapfr := newFrame(fr.unit, fn)
	defer wrapfr.dispose()

	// TODO(growly): we don't need to unpack the void* arg
	//prologuebb := llvm.AddBasicBlock(fn, "prologue")
	//wrapfr.builder.SetInsertPointAtEnd(prologuebb)
	//arg := fn.Param(0)
	//arg = fr.builder.CreateBitCast(arg, structllptr, "")
	//for i, ssaarg := range args {
	//	argptr := thunkfr.builder.CreateStructGEP(arg, i, "")
	//	arg := thunkfr.builder.CreateLoad(argptr, "")
	//	arg.env[ssaarg] = newValue(arg, ssaarg.Type())
	//}

	entrybb := llvm.AddBasicBlock(fn, "entry")
	//br := wrapfr.builder.CreateBr(entrybb)
	//wrapfr.allocaBuilder.SetInsertPointBefore(br)

	wrapfr.builder.SetInsertPointAtEnd(entrybb)

	//args := []llvm.Value{llvm.Undef(int8ptr)}

	//argtyp := llvm.PointerType(llvm.Int8Type(), 0)
	//argcopy := fr.allocaBuilder.CreateAlloca(argtyp, "")
	//argcopy = innerfn.Param(0)
	wrapfr.builder.CreateCall(innerfn, []llvm.Value{fn.Param(0)}, "")

	null_ptr := llvm.ConstNull(llvm.PointerType(llvm.Int8Type(), 0))
	wrapfr.runtime.pthreadExit.call(wrapfr, null_ptr)

	wrapfr.builder.CreateRet(null_ptr)
	return fr.builder.CreateBitCast(fn, i8ptr, "")
}
