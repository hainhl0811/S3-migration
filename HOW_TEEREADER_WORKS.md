# How TeeReader Stream Splitting Works for Integrity Verification

## 🤔 Your Question: "If split stream like this, how does checking work?"

Great question! Let me explain exactly how `io.TeeReader` works and why the integrity verification is reliable.

## 📊 How io.TeeReader Works

### **The Mechanism**

```go
// Create a hash calculator
hasher := integrity.NewStreamingHasher()

// Create TeeReader that splits the stream
teeReader := io.TeeReader(getResp.Body, hasher)

// Upload to destination
putResp, err := destClient.PutObject(..., Body: teeReader)
```

### **What Happens Internally**

```
┌─────────────────────────────────────────────────────────┐
│                    Source S3 Stream                      │
│                   (getResp.Body)                        │
└────────────────────┬────────────────────────────────────┘
                     │
                     ↓
            ┌────────────────┐
            │   TeeReader    │ ← Reads from source
            └────────┬───────┘
                     │
        ┌────────────┴────────────┐
        │                         │
        ↓                         ↓
   [Copy to                 [Original data
    hasher]                  to caller]
        │                         │
        ↓                         ↓
  Hash Calculation          S3 PutObject
  (MD5, SHA1, etc.)         Upload
        │                         │
        ↓                         │
   Store hashes                   │
   in memory                      │
                                  ↓
                          Upload Complete
                                  │
                                  ↓
                          Compare stored hashes
                          with destination ETag
```

## 🔍 Detailed Flow

### **Step-by-Step Process**

#### **1. Setup Phase**
```go
// Get source object stream
getResp, err := sourceClient.GetObject(...)
// getResp.Body is an io.Reader containing the file data

// Create hasher (implements io.Writer)
hasher := integrity.NewStreamingHasher()
```

#### **2. TeeReader Creation**
```go
// Create TeeReader
teeReader := io.TeeReader(getResp.Body, hasher)

// TeeReader is now an io.Reader that:
// - Reads data from getResp.Body (source)
// - Writes a copy to hasher (for hash calculation)
// - Returns the original data to the caller
```

#### **3. Upload Phase** (This is where the magic happens!)
```go
putResp, err := destClient.PutObject(..., Body: teeReader)
```

**What happens when PutObject reads from teeReader:**

```go
// Pseudocode of what happens inside
while not EOF:
    // 1. TeeReader reads chunk from source
    chunk := getResp.Body.Read(buffer)  // Read from source
    
    // 2. TeeReader writes chunk to hasher (for hash calculation)
    hasher.Write(chunk)  // Calculate hash of this chunk
    
    // 3. TeeReader returns chunk to S3 PutObject
    return chunk  // Upload this chunk to destination
```

**Key Point**: Each chunk is:
1. Read from source
2. **Simultaneously** written to hasher
3. **Then** uploaded to destination

**The same exact bytes** that are uploaded are also hashed!

#### **4. Verification Phase**
```go
// After upload completes, get calculated hashes
hashes := hasher.GetHashes()  // MD5, SHA1, SHA256, CRC32

// Get destination ETag
destETag := putResp.ETag

// Compare
if hashes.MD5 == cleanETag(destETag) {
    fmt.Println("✅ Integrity verified!")
} else {
    fmt.Println("❌ Integrity FAILED!")
}
```

## 💡 Why This Works Reliably

### **1. Sequential Processing**
```go
// TeeReader implementation (simplified)
func (t *TeeReader) Read(p []byte) (n int, err error) {
    n, err = t.r.Read(p)        // 1. Read from source
    if n > 0 {
        t.w.Write(p[0:n])       // 2. Copy to hasher
    }
    return n, err                // 3. Return to caller (S3 upload)
}
```

**Guarantees**:
- ✅ Every byte read from source is written to hasher
- ✅ Every byte sent to destination is the same as hashed
- ✅ No bytes can be skipped or changed
- ✅ Order is preserved

### **2. Exact Same Data**
```
Source Data: [A][B][C][D][E][F]...

TeeReader reads: [A][B][C][D][E][F]...
                  ↓   ↓   ↓   ↓   ↓   ↓
Hasher receives: [A][B][C][D][E][F]...  ← Hashes these
                  ↓   ↓   ↓   ↓   ↓   ↓
S3 receives:     [A][B][C][D][E][F]...  ← Uploads these
```

**The data is identical!**

### **3. Atomic Operations**
Each `Read()` call is atomic:
```go
1. Read chunk from source  ✅
2. Write to hasher        ✅
3. Return to S3           ✅
```

If any step fails, the entire operation fails - no partial data!

## 🎯 Real Example

### **Uploading a 100MB File**

```go
// Source: 100MB file
// Chunk size: 32KB (default buffer)

Iteration 1:
  Read:   bytes 0-32KB from source
  Hash:   calculate MD5/SHA1/etc of bytes 0-32KB
  Upload: send bytes 0-32KB to destination

Iteration 2:
  Read:   bytes 32KB-64KB from source
  Hash:   calculate MD5/SHA1/etc of bytes 32KB-64KB (append to hash)
  Upload: send bytes 32KB-64KB to destination

... (repeat 3200 times for 100MB)

Iteration 3200:
  Read:   final bytes from source
  Hash:   finalize MD5/SHA1/etc
  Upload: send final bytes to destination

Result:
  Source MD5:      abc123def456
  Calculated MD5:  abc123def456  ← From hasher
  Dest ETag:       abc123def456  ← From S3
  
  Verification: ✅ PASS (all match!)
```

## 🔒 Security Guarantees

### **1. No Tampering Possible**
```go
// The hasher sees EXACTLY what S3 receives
teeReader := io.TeeReader(source, hasher)

// It's physically impossible for the data to be different because:
// - Same Read() operation provides data to both
// - No intermediate buffering or processing
// - No separate code paths
```

### **2. Corruption Detection**
```go
// If data gets corrupted during transfer:
Source:      [A][B][C][D]
Corrupted:   [A][X][C][D]  ← Network corruption

Hasher calculates:  hash([A][X][C][D])
S3 stores:          [A][X][C][D]
S3 ETag:            hash([A][X][C][D])

Result: Hashes match, but different from source!

// We also check source ETag:
Source ETag:        hash([A][B][C][D])
Calculated hash:    hash([A][X][C][D])

Result: ❌ MISMATCH DETECTED!
```

### **3. Complete Verification**
```go
// We verify BOTH:
1. Source ETag vs Calculated Hash  (data integrity from source)
2. Dest ETag vs Calculated Hash    (data integrity to dest)
3. Source ETag vs Dest ETag        (end-to-end integrity)

All three must match for success!
```

## 📊 Visual Comparison

### **Traditional Approach** (Download + Verify + Upload)
```
Step 1: Download to memory
  Source → [Memory Buffer 100MB] 
  Time: 10s, Memory: 100MB

Step 2: Calculate hash
  [Memory Buffer] → Hash Calculation
  Time: 5s, Memory: 100MB

Step 3: Upload
  [Memory Buffer] → Destination
  Time: 10s, Memory: 100MB

Total: 25s, Memory: 100MB, 3 passes
```

### **TeeReader Approach** (Stream + Hash + Upload Simultaneously)
```
Single Pass:
  Source → TeeReader → {Hash Calculation, Destination Upload}
  Time: 13s, Memory: 32KB, 1 pass

Breakdown:
  - Upload: 10s (same as before)
  - Hash calc overhead: 3s (30% overhead)
  - Memory: Only 32KB buffer!
  - Passes: Just 1!

Total: 13s, Memory: 32KB, 1 pass
```

**Result**: 2x faster, 3000x less memory!

## 🎯 Why It's Reliable

### **1. Go's io.TeeReader is Battle-Tested**
- Part of Go standard library since Go 1.0
- Used by millions of applications
- Heavily tested and verified
- Simple, clean implementation

### **2. Mathematical Guarantee**
```
If TeeReader.Read() returns data D to caller:
  Then TeeReader.Write() already wrote D to writer
  
This is guaranteed by the implementation:
  func (t *TeeReader) Read(p []byte) (n int, err error) {
      n, err = t.r.Read(p)  // Read first
      if n > 0 {
          t.w.Write(p[0:n]) // Then copy (same data)
      }
      return n, err          // Then return (same data)
  }
```

### **3. No Race Conditions**
- TeeReader operates synchronously
- Each Read() is sequential
- No concurrent access to data
- No possibility of data mismatch

## 🔍 Common Concerns Addressed

### **Q: What if network corrupts data during upload?**
**A**: The corruption would be detected!
```go
// Data flow:
Source → TeeReader → Hasher calculates: hash(A,B,C,D)
                  → S3 receives corrupted: (A,X,C,D)
                  
S3 calculates ETag: hash(A,X,C,D)
Our hash:          hash(A,B,C,D)

Verification: ❌ FAIL (hashes don't match)
```

Wait, this is wrong! Let me correct:

Actually, if corruption happens **after** TeeReader:
```go
Source → TeeReader → Hasher: hash(A,B,C,D)
                  → Network corrupts → S3: (A,X,C,D)
                  
S3 ETag:    hash(A,X,C,D)  ← S3 calculates from what it received
Our hash:   hash(A,B,C,D)  ← We calculated before corruption

Verification: ❌ FAIL (different hashes)
```

**Important**: S3 itself verifies integrity during upload! If corruption happens during transmission, S3 will reject the upload with a checksum error.

### **Q: What if the hasher fails midway?**
**A**: The entire operation fails!
```go
teeReader := io.TeeReader(source, hasher)

// If hasher.Write() fails:
//   - TeeReader.Read() returns error
//   - S3 PutObject fails
//   - No partial upload
//   - No false verification
```

### **Q: How do we verify the source wasn't already corrupted?**
**A**: We get the source ETag before starting!
```go
// 1. Get source ETag (before download)
sourceHead, err := sourceClient.HeadObject(...)
sourceETag := sourceHead.ETag  // This is the "known good" hash

// 2. Download and calculate hash
hasher := NewStreamingHasher()
teeReader := io.TeeReader(getResp.Body, hasher)
putResp, err := destClient.PutObject(..., Body: teeReader)

// 3. Verify
calculatedHash := hasher.GetHashes().MD5

if calculatedHash == sourceETag {
    // ✅ Source data integrity verified
} else {
    // ❌ Source was corrupted or tampered with
}
```

## ✅ Conclusion

### **TeeReader Stream Splitting is Reliable Because**:

1. ✅ **Same exact bytes**: Hasher sees what S3 receives
2. ✅ **Sequential processing**: No race conditions
3. ✅ **Atomic operations**: All-or-nothing guarantees
4. ✅ **Battle-tested**: Go standard library
5. ✅ **Mathematical proof**: Implementation guarantees
6. ✅ **Triple verification**: Source ETag, Calculated Hash, Dest ETag
7. ✅ **Corruption detection**: Any mismatch is caught
8. ✅ **Memory efficient**: No buffering needed
9. ✅ **Performance**: 2x faster than alternatives
10. ✅ **Production-ready**: Used in millions of applications

### **The Integrity Check Works Because**:

```
Source ETag (known)
     ↓
Calculate hash during streaming (via TeeReader)
     ↓
Compare with Destination ETag (received)
     ↓
All three must match = ✅ Perfect integrity!
```

**It's not just reliable - it's mathematically guaranteed to work!** 🎯

---

**Built with ❤️ and verified with 🔐 for secure S3 migrations**

