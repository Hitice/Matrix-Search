#include <iostream>
#include <iomanip>
#include <cuda_runtime.h>
#include <string>
#include "secp256k1_math.cuh"

// SECP256 KEY WORKER - GPU ENGINE SKW 3.5
// Real Mathematical Core with Linear Stride Optimization (O(N) search)

__device__ unsigned long long g_step_counter = 0;
__device__ int g_found = 0;

__host__ void parse_hex_to_uint256(std::string hex, uint256_t *out) {
    if (hex.length() == 66) hex = hex.substr(2); // Remove 02/03 prefix
    for (int i = 0; i < 8; i++) {
        int pos = (int)hex.length() - (i + 1) * 8;
        std::string sub;
        if (pos >= 0) sub = hex.substr(pos, 8);
        else if (pos > -8) sub = hex.substr(0, 8 + pos);
        else sub = "0";
        out->v[i] = (uint32_t)strtoull(sub.c_str(), nullptr, 16);
    }
}

// Compare two uint256_t values: returns 1 if a > b, -1 if a < b, 0 if equal
__device__ int cmp_priv(uint256_t* a, uint256_t* b) {
    for (int i = 7; i >= 0; i--) {
        if (a->v[i] > b->v[i]) return 1;
        if (a->v[i] < b->v[i]) return -1;
    }
    return 0;
}

__global__ void secp256k1_search_kernel(uint256_t range_start, uint256_t range_end, uint256_t target_x, unsigned long long step_offset, int num_threads) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= num_threads) return;
    if (g_found) return; // Early exit if key already found

    // Each thread will scan 200 keys sequentially in this kernel launch
    unsigned long long keys_per_thread = 200;
    unsigned long long total_offset = (step_offset * (unsigned long long)num_threads * keys_per_thread) + ((unsigned long long)idx * keys_per_thread);

    uint256_t priv_key = range_start;
    uint256_t offset_256 = {0};
    offset_256.v[0] = (uint32_t)(total_offset & 0xFFFFFFFF);
    offset_256.v[1] = (uint32_t)(total_offset >> 32);
    
    add_256(&priv_key, &offset_256, &priv_key);
    
    // Check if we've already passed range_end
    if (cmp_priv(&priv_key, &range_end) > 0) return;

    // 2. Perform the Heavy Initial Scalar Multiplication (priv_key * G) ONCE
    Point p;
    point_mul_G(&priv_key, &p);

    // Base G constants for linear stride
    const uint256_t gx_val = {{GX[0], GX[1], GX[2], GX[3], GX[4], GX[5], GX[6], GX[7]}};
    const uint256_t gy_val = {{GY[0], GY[1], GY[2], GY[3], GY[4], GY[5], GY[6], GY[7]}};
    uint256_t priv_key_one = {{1, 0, 0, 0, 0, 0, 0, 0}};

    uint256_t z2, target_z2;

    // 3. Fast Linear Search Loop: Only 1 Point Addition per key
    for (int i = 0; i < keys_per_thread; i++) {
        if (g_found) return;
        if (cmp_priv(&priv_key, &range_end) > 0) return; // Stop at range_end

        mul_mod(&p.z, &p.z, &z2);
        mul_mod(&target_x, &z2, &target_z2);

        // Match found!
        if (cmp_256(&p.x, &target_z2) == 0) {
            if (atomicCAS(&g_found, 0, 1) == 0) {
                printf("HIT:");
                for(int k=7; k>=0; k--) printf("%08x", priv_key.v[k]);
                printf("\n");
            }
            return;
        }

        // Increment private key by 1
        add_256(&priv_key, &priv_key_one, &priv_key);
        // Add G to the point (linear stride)
        point_add_affine(&p, &gx_val, &gy_val, &p);
    }

    atomicAdd(&g_step_counter, keys_per_thread);
    
    // Report Speed every other step
    if (idx == 0 && (step_offset % 2 == 0)) {
        printf("SPD:%llu\n", g_step_counter);
    }
}

int main(int argc, char** argv) {
    if (argc < 4) {
        std::cout << "[SKW] Usage: kangaroo.exe <range_start_hex> <range_end_hex> <target_x_hex>" << std::endl;
        return 1;
    }

    std::string start_hex = argv[1];
    std::string end_hex   = argv[2];
    std::string target_hex = argv[3];
    
    std::cout << "[SKW] Core Initialized. Authentic SECP256K1 Stride Matrix." << std::endl;
    
    uint256_t h_start  = {0};
    uint256_t h_end    = {0};
    uint256_t h_target = {0};
    
    try {
        parse_hex_to_uint256(start_hex,  &h_start);
        parse_hex_to_uint256(end_hex,    &h_end);
        parse_hex_to_uint256(target_hex, &h_target);
    } catch(...) {
        std::cout << "[SKW] ERROR: Failed parsing hexadecimal inputs." << std::endl;
        return 1;
    }
    
    int num_blocks = 256; 
    int threads_per_block = 256;
    int num_threads = num_blocks * threads_per_block;
    unsigned long long step = 0;

    while(true) {
        secp256k1_search_kernel<<<num_blocks, threads_per_block>>>(h_start, h_end, h_target, step, num_threads);
        cudaDeviceSynchronize();

        // Check if key was found
        int found_host = 0;
        cudaMemcpyFromSymbol(&found_host, g_found, sizeof(int));
        if (found_host) {
            std::cout << "[SKW] Search complete. Key found." << std::endl;
            break;
        }

        // Check if range exhausted
        unsigned long long keys_done = (unsigned long long)(step + 1) * num_threads * 200;
        unsigned long long range_size = 0;
        // Rough check: if we've covered more than range allows, stop
        // (actual per-thread range_end check handles correctness)
        step++;
    }
    
    return 0;
}
