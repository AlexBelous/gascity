/* Small self-contained SHA-256. No filesystem or crypto-provider loading. */
#ifndef GC_OBSERVER_SHA256_H
#define GC_OBSERVER_SHA256_H
#include <stddef.h>
#include <stdint.h>
#include <string.h>
typedef struct {
  uint32_t h[8];
  uint64_t bytes;
  unsigned char b[64];
  size_t n;
} sha256_ctx;
static uint32_t ror32(uint32_t x, unsigned n) {
  return (x >> n) | (x << (32 - n));
}
static void sha256_block(sha256_ctx *c, const unsigned char *b) {
  static const uint32_t k[64] = {
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1,
      0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
      0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786,
      0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147,
      0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
      0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
      0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a,
      0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
      0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2};
  uint32_t w[64];
  for (int i = 0; i < 16; i++)
    w[i] = ((uint32_t)b[i * 4] << 24) | ((uint32_t)b[i * 4 + 1] << 16) |
           ((uint32_t)b[i * 4 + 2] << 8) | b[i * 4 + 3];
  for (int i = 16; i < 64; i++) {
    uint32_t a = w[i - 15], z = w[i - 2];
    w[i] = w[i - 16] + (ror32(a, 7) ^ ror32(a, 18) ^ (a >> 3)) + w[i - 7] +
           (ror32(z, 17) ^ ror32(z, 19) ^ (z >> 10));
  }
  uint32_t a = c->h[0], b0 = c->h[1], d = c->h[3], e = c->h[4], f = c->h[5],
           g = c->h[6], h = c->h[7], cc = c->h[2];
  for (int i = 0; i < 64; i++) {
    uint32_t t = h + (ror32(e, 6) ^ ror32(e, 11) ^ ror32(e, 25)) +
                 ((e & f) ^ ((~e) & g)) + k[i] + w[i];
    uint32_t u = (ror32(a, 2) ^ ror32(a, 13) ^ ror32(a, 22)) +
                 ((a & b0) ^ (a & cc) ^ (b0 & cc));
    h = g;
    g = f;
    f = e;
    e = d + t;
    d = cc;
    cc = b0;
    b0 = a;
    a = t + u;
  }
  c->h[0] += a;
  c->h[1] += b0;
  c->h[2] += cc;
  c->h[3] += d;
  c->h[4] += e;
  c->h[5] += f;
  c->h[6] += g;
  c->h[7] += h;
}
static void sha256_init(sha256_ctx *c) {
  static const uint32_t h[] = {0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
                               0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19};
  memcpy(c->h, h, sizeof h);
  c->bytes = 0;
  c->n = 0;
}
static void sha256_update(sha256_ctx *c, const void *v, size_t n) {
  const unsigned char *p = v;
  c->bytes += n;
  while (n) {
    size_t z = 64 - c->n;
    if (z > n)
      z = n;
    memcpy(c->b + c->n, p, z);
    c->n += z;
    p += z;
    n -= z;
    if (c->n == 64) {
      sha256_block(c, c->b);
      c->n = 0;
    }
  }
}
static void sha256_hex(sha256_ctx *c, char out[65]) {
  uint64_t bits = c->bytes * 8;
  unsigned char q = 0x80;
  sha256_update(c, &q, 1);
  q = 0;
  while (c->n != 56)
    sha256_update(c, &q, 1);
  unsigned char len[8];
  for (int i = 0; i < 8; i++)
    len[7 - i] = (unsigned char)(bits >> (i * 8));
  sha256_update(c, len, 8);
  static const char hx[] = "0123456789abcdef";
  for (int i = 0; i < 32; i++) {
    unsigned char x = (unsigned char)(c->h[i / 4] >> (24 - (i % 4) * 8));
    out[i * 2] = hx[x >> 4];
    out[i * 2 + 1] = hx[x & 15];
  }
  out[64] = 0;
}
static void sha256_sum(const void *v, size_t n, char out[65]) {
  sha256_ctx c;
  sha256_init(&c);
  sha256_update(&c, v, n);
  sha256_hex(&c, out);
}
#endif
