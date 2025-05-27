#!/usr/bin/env python3
import requests
import time
import random
import threading
import json
from concurrent.futures import ThreadPoolExecutor
import argparse

class TraefikTester:
    def __init__(self, base_url="http://jsonapi.example.local", duration=300, rps=2):
        self.base_url = base_url
        self.duration = duration
        self.rps = rps
        self.request_count = 0
        self.start_time = time.time()

        self.endpoints = [
            ("GET", "/api/users"),
            ("GET", "/api/users/123"),
            ("GET", "/api/users/456"),
            ("GET", "/api/users/789"),
            ("GET", "/api/v1/products"),
            ("GET", "/api/v1/products/abc-123"),
            ("GET", "/api/v2/products"),
            ("GET", "/api/v2/products/def-456"),
            ("GET", "/api/users/123/orders"),
            ("GET", "/api/users/456/orders/789"),
            ("GET", "/api/orders/550e8400-e29b-41d4-a716-446655440000"),
            ("GET", "/health"),
            ("GET", "/health/ready"),
            ("GET", "/admin/dashboard"),
            ("GET", "/nonexistent"),  # 404 error
            ("POST", "/api/users"),
            ("PUT", "/api/users/123"),
            ("DELETE", "/api/users/123"),
        ]

    def make_request(self, method, path):
        url = f"{self.base_url}{path}"

        try:
            start_time = time.time()

            if method == "GET":
                response = requests.get(url, headers={"Accept": "application/json"}, timeout=10)
            elif method == "POST":
                data = {"name": "Test User", "email": "test@example.com"}
                response = requests.post(url, json=data, timeout=10)
            elif method == "PUT":
                data = {"name": "Updated User", "email": "updated@example.com"}
                response = requests.put(url, json=data, timeout=10)
            elif method == "DELETE":
                response = requests.delete(url, timeout=10)

            response_time = time.time() - start_time
            self.request_count += 1

            print(f"[{time.strftime('%H:%M:%S')}] #{self.request_count:04d} {method:6} {path:30} "
                  f"Status: {response.status_code:3d} Time: {response_time:.3f}s")

        except requests.exceptions.RequestException as e:
            print(f"Error: {method} {path} - {str(e)}")

    def run_continuous_test(self):
        print(f"Starting continuous test for {self.duration}s at {self.rps} RPS")
        print("Press Ctrl+C to stop early")

        try:
            while time.time() - self.start_time < self.duration:
                method, path = random.choice(self.endpoints)
                self.make_request(method, path)
                time.sleep(1.0 / self.rps)

        except KeyboardInterrupt:
            print("\nTest interrupted by user")

        print(f"\nTest completed. Total requests: {self.request_count}")
        print("Check metrics at: http://localhost:9090/metrics")

    def run_parallel_test(self, threads=4):
        print(f"Starting parallel test with {threads} threads for {self.duration}s")

        def worker():
            while time.time() - self.start_time < self.duration:
                method, path = random.choice(self.endpoints)
                self.make_request(method, path)
                time.sleep(random.uniform(0.5, 2.0))  # Random delay between requests

        with ThreadPoolExecutor(max_workers=threads) as executor:
            futures = [executor.submit(worker) for _ in range(threads)]

            try:
                for future in futures:
                    future.result()
            except KeyboardInterrupt:
                print("\nTest interrupted by user")

        print(f"\nParallel test completed. Total requests: {self.request_count}")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Test Traefik URL Latency Plugin")
    parser.add_argument("--duration", type=int, default=300, help="Test duration in seconds")
    parser.add_argument("--rps", type=float, default=2.0, help="Requests per second")
    parser.add_argument("--parallel", action="store_true", help="Run parallel test")
    parser.add_argument("--threads", type=int, default=4, help="Number of parallel threads")

    args = parser.parse_args()

    tester = TraefikTester(duration=args.duration, rps=args.rps)

    if args.parallel:
        tester.run_parallel_test(threads=args.threads)
    else:
        tester.run_continuous_test()