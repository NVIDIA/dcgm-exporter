# Prerequisites

- Machine with NVIDIA GPU
- Operation system Linux
- [Golang >= 1.24 installed](https://golang.org/)
- [DCGM installed](https://developer.nvidia.com/dcgm)

# Integration Tests

## Basics

From the dcgm-exporter root directory run the tests:

```
make test-integration
```

## Quickly iterating on a single test

Run a single test

```
make test-integration -e TEST_ARGS="-test.run [test name here]"
```

Example:
```
make test-integration -e TEST_ARGS="-test.run TestStartAndReadMetrics"
```

**WARNING**: It takes about 30 seconds, before the dcgm-exporter instance will read available metrics. Some metrics require at least two data points to compute a value, meaning at least one polling interval should be passed before we can get the results. By default, dcgm-exporter uses 30-second polling intervals, thus the delay we observe.


# Testing Philosophy

* Assumed that tests can be run on any Linux machine with compatible NVIDIA GPU
* Tests are the best documentation.
* The reader should easily read and understand the tested scenario.
* One file must contain only one test scenario.
