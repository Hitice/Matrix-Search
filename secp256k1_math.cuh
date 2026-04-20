// secp256k1_math.cuh
#ifndef SECP256K1_MATH_CUH
#define SECP256K1_MATH_CUH

#include <stdint.h>

struct uint256_t {
    uint32_t v[8];
};

__device__ __constant__ uint32_t P[8] = {
    0xFFFFFC2F, 0xFFFFFFFE, 0xFFFFFFFF, 0xFFFFFFFF,
    0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF
};

__device__ __constant__ uint32_t GX[8] = {
    0x16F81798, 0x59F2815B, 0x2DCE28D9, 0x029BFCDB,
    0xCE870B07, 0x55A06295, 0xF9DCBBAC, 0x79BE667E
};

__device__ __constant__ uint32_t GY[8] = {
    0xFB10D4B8, 0x9C47D08F, 0xA6855419, 0xFD17B448,
    0x0E1108A8, 0x5DA4FBFC, 0x26A3C465, 0x483ADA77
};

__device__ int cmp_256(const uint256_t* a, const uint256_t* b) {
    for (int i = 7; i >= 0; i--) {
        if (a->v[i] > b->v[i]) return 1;
        if (a->v[i] < b->v[i]) return -1;
    }
    return 0;
}

__device__ int add_256(const uint256_t* a, const uint256_t* b, uint256_t* r) {
    uint64_t c = 0;
    #pragma unroll
    for (int i = 0; i < 8; i++) {
        c += (uint64_t)a->v[i] + b->v[i];
        r->v[i] = (uint32_t)c;
        c >>= 32;
    }
    return (int)c;
}

__device__ int sub_256(const uint256_t* a, const uint256_t* b, uint256_t* r) {
    int64_t c = 0;
    #pragma unroll
    for (int i = 0; i < 8; i++) {
        c += (int64_t)a->v[i] - b->v[i];
        r->v[i] = (uint32_t)c;
        c >>= 32;
    }
    return (int)(c < 0 ? 1 : 0);
}

__device__ void add_mod(const uint256_t* a, const uint256_t* b, uint256_t* r) {
    int carry = add_256(a, b, r);
    if (carry || cmp_256(r, (const uint256_t*)P) >= 0) {
        sub_256(r, (const uint256_t*)P, r);
    }
}

__device__ void sub_mod(const uint256_t* a, const uint256_t* b, uint256_t* r) {
    if (cmp_256(a, b) >= 0) {
        sub_256(a, b, r);
    } else {
        uint256_t tmp;
        sub_256(a, b, &tmp);
        add_256(&tmp, (const uint256_t*)P, r);
    }
}

__device__ void mul_mod(const uint256_t* a, const uint256_t* b, uint256_t* r) {
    uint32_t t[16] = {0};
    
    // 1. Branchless 512-bit Multiplication
    #pragma unroll
    for(int i=0; i<8; i++) {
        uint64_t c = 0;
        #pragma unroll
        for(int j=0; j<8; j++) {
            c += (uint64_t)a->v[i] * b->v[j] + t[i+j];
            t[i+j] = (uint32_t)c;
            c >>= 32;
        }
        t[i+8] = (uint32_t)c;
    }
    
    // 2. Fast Satoshi Reduction modulo (2^256 - 2^32 - 977)
    #pragma unroll
    for (int k=0; k<2; k++) { 
        uint64_t c = 0;
        #pragma unroll
        for(int i=0; i<8; i++) {
            uint32_t H = t[8+i];
            t[8+i] = 0; // clear
            c += (uint64_t)t[i] + (uint64_t)H * 0x3D1ULL;
            t[i] = (uint32_t)c;
            c >>= 32;
            c += H; 
        }
        t[8] = (uint32_t)c;
        t[9] = (uint32_t)(c >> 32); 
    }
    
    // 3. Final pass to fold the leftover carry (t[8]) back into the field
    if (t[8] > 0) {
        uint64_t c = (uint64_t)t[0] + (uint64_t)t[8] * 0x3D1ULL;
        t[0] = (uint32_t)c;
        c >>= 32;
        c += t[8]; 
        
        #pragma unroll
        for (int i=1; i<8; i++) {
            c += t[i];
            t[i] = (uint32_t)c;
            c >>= 32;
        }
    }
    
    for(int i=0; i<8; i++) r->v[i] = t[i];
    
    // Final check to handle bounds [P, 2^256 - 1]
    if (cmp_256(r, (const uint256_t*)P) >= 0) {
        sub_256(r, (const uint256_t*)P, r);
    }
}

struct Point {
    uint256_t x, y, z;
};

__device__ void point_double(Point* p, Point* r) {
    uint256_t xx, yy, yyyy, s, m, tmp, tmp2;
    mul_mod(&p->x, &p->x, &xx);
    mul_mod(&p->y, &p->y, &yy);
    mul_mod(&yy, &yy, &yyyy);
    
    mul_mod(&p->x, &yy, &s);
    add_mod(&s, &s, &tmp); 
    add_mod(&tmp, &tmp, &s); 
    
    add_mod(&xx, &xx, &tmp);
    add_mod(&xx, &tmp, &m);
    
    mul_mod(&m, &m, &tmp2);
    add_mod(&s, &s, &tmp);
    uint256_t rx, ry, rz;
    sub_mod(&tmp2, &tmp, &rx);
    
    sub_mod(&s, &rx, &tmp);
    mul_mod(&m, &tmp, &tmp2);
    add_mod(&yyyy, &yyyy, &tmp);
    add_mod(&tmp, &tmp, &tmp);
    add_mod(&tmp, &tmp, &tmp);
    
    // Compute Z3 before overwriting Y
    mul_mod(&p->y, &p->z, &rz);
    add_mod(&rz, &rz, &rz);
    
    sub_mod(&tmp2, &tmp, &ry);
    
    r->x = rx;
    r->y = ry;
    r->z = rz;
}

__device__ void point_add_affine(Point* p1, const uint256_t* px2, const uint256_t* py2, Point* r) {
    uint256_t z1z1, u2, s2, h, hh, hhh, r_term, v;
    mul_mod(&p1->z, &p1->z, &z1z1);
    mul_mod(px2, &z1z1, &u2);
    
    mul_mod(&z1z1, &p1->z, &s2);
    mul_mod(py2, &s2, &s2);
    
    sub_mod(&u2, &p1->x, &h);
    sub_mod(&s2, &p1->y, &r_term);
    
    int is_zero = 1;
    #pragma unroll
    for(int i=0; i<8; i++) if(h.v[i] != 0) is_zero = 0;
    
    if (is_zero) {
        point_double(p1, r);
        return;
    }
    
    mul_mod(&h, &h, &hh);
    mul_mod(&h, &hh, &hhh);
    mul_mod(&p1->x, &hh, &v);
    
    mul_mod(&r_term, &r_term, &u2); 
    sub_mod(&u2, &hhh, &s2); 
    
    // We need 2*v to subtract. Let's store it in a temp variable, say `u2` (since we just consumed it)
    uint256_t two_v, rx, ry, rz;
    add_mod(&v, &v, &two_v); 
    sub_mod(&s2, &two_v, &rx);
    
    sub_mod(&v, &rx, &s2);
    mul_mod(&r_term, &s2, &u2);
    mul_mod(&p1->y, &hhh, &s2);
    
    // Compute Z3 before overwriting Y
    mul_mod(&p1->z, &h, &rz);
    
    sub_mod(&u2, &s2, &ry);
    
    r->x = rx;
    r->y = ry;
    r->z = rz;
}

__device__ void point_mul_G(const uint256_t* k, Point* r) {
    Point res = {{0}, {0}, {0}};
    Point base = {
        {{GX[0], GX[1], GX[2], GX[3], GX[4], GX[5], GX[6], GX[7]}},
        {{GY[0], GY[1], GY[2], GY[3], GY[4], GY[5], GY[6], GY[7]}},
        {{1, 0, 0, 0, 0, 0, 0, 0}}
    };
    
    int first_bit = 1;

    for (int i = 7; i >= 0; i--) {
        uint32_t val = k->v[i];
        for (int b = 31; b >= 0; b--) {
            int bit = (val >> b) & 1;
            
            if (!first_bit) {
                point_double(&res, &res);
            }
            
            if (bit) {
                if (first_bit) {
                    res = base;
                    first_bit = 0;
                } else {
                    Point tmp;
                    point_add_affine(&res, &base.x, &base.y, &tmp);
                    res = tmp;
                }
            }
        }
    }
    *r = res;
}

#endif
