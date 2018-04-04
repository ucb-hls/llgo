
# UCB-HLS Build notes

## llgo

This works at HEAD. Configure with cmake and build with ninja (`ninja-build` package on ubuntu).

On an Amazon (...) EC2 instance it works fine, but in my home virtual machine I get errors about compiling with -fPIC even though cmake claims to enable PIC.

Build steps:
```
export TOP=$(pwd)
git clone https://git.llvm.org/git/llvm.git/ llvm
cd ${TOP}/llvm/tools
git clone https://git.llvm.org/git/clang.git/
git clone https://git.llvm.org/git/llgo.git/
cd ${TOP}
mkdir llvm-build
cd llvm-build
cmake -GNinja ${TOP}/llvm
ninja
# Remember the directory we're in as the directory into which we built LLVM at HEAD (for llgo)
export LLVM_HEAD_BUILD=${TOP}/llvm-build
```

Use:
1. `${LLVM_HEAD_BUILD}/bin/llgo -S -emit-llvm <go_source_file_name>.go`
2. Remove the 2nd line in the file, `source_filename = "main"` (or equivalent)
3. Remember where the emitted file is - it'll be called `<go_source_file_name>.s`

### Go Runtime APIs
APIs: 
[https://github.com/llvm-mirror/llgo/blob/ff92724c045e4856191d137bdda914e1b5de8950/irgen/runtime.go](https://github.com/llvm-mirror/llgo/blob/ff92724c045e4856191d137bdda914e1b5de8950/irgen/runtime.go)

Implementation:
[https://github.com/llvm-mirror/llgo/blob/de4db9f8144f40014e8b32d263a91478e6f1a21f/third_party/gofrontend/libgo/runtime/chan.goc#L1](https://github.com/llvm-mirror/llgo/blob/de4db9f8144f40014e8b32d263a91478e6f1a21f/third_party/gofrontend/libgo/runtime/chan.goc#L1)

LLVM Go Binding:
https://github.com/go-llvm/llvm/blob/c8914dc5244584970ee28d046064ce246890cd69/core.go 

Go SSA Viewer:
https://golang-ssaview.herokuapp.com/

Go AST Viewer: 
http://goast.yuroyoro.net/

Examples: Type `make`  under the corresponding folders
1. Unbuferred Channel (additional go_new function)
 __go_new_channel(/*UNDEF*/((uint8_t*)/*NULL*/0), ((&__go_td_CN3_intsre.field0.field0)), UINT64_C(0));
 __go_new(/*UNDEF*/((uint8_t*)/*NULL*/0), ((&__go_td_S0_CN3_intsree.field0.field0)), UINT64_C(8));

2. Bufferd Channel
	See most examples

3. Close Channel
	go_builtin_close
4. Range Channel 
  llvm_cbe_tmp__11 = runtime_OC_chanrecv2(/*UNDEF*/((uint8_t*)/*NULL*/0), ((&__go_td_CN6_stringsre.field0.field0)), llvm_cbe_tmp__10, (((uint8_t*)(&llvm_cbe_tmp__7)))); // in a loop

5. Select
uint8_t* runtime_OC_newselect(uint8_t*, uint32_t);
uint64_t runtime_OC_selectgo(uint8_t*, uint8_t*);
void runtime_OC_selectrecv2(uint8_t*, uint8_t*, uint8_t*, uint8_t*, uint8_t*, uint32_t);

6. Worker Pool
its own function main_OC_worker



More go examples:
[https://github.com/avelino/awesome-go#benchmarks](https://github.com/avelino/awesome-go#benchmarks)


### Go Debuggging
In llgo, add: 
fr.module.Module.Dump()
fn.Dump()

### Go IDE
GoLand https://www.jetbrains.com/go/

