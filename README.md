<p align="center">
  <img src="./docs/pics/logo.png" width="600" alt="Foliage Logo">
</p>

Foliage is a collaborative application platform built around a distributed graph database, offering a common and extensible environment for seamless automation, inter-domain connectivity, and high-performance, edge-friendly runtimes. 

[![License][License-Image]][License-Url]

[License-Url]: https://www.apache.org/licenses/LICENSE-2.0
[License-Image]: https://img.shields.io/badge/License-Apache2-blue.svg

## Table of Contents

- [Core concepts](#core-concepts)
  - [Abstraction](#abstraction)
  - [Features](#features)
- [Getting Started](#getting-started)
  - [Included tests runtime](#included-tests-runtime)
    - [1. Go to `tests` dir](#1-go-to-tests-dir)
    - [2. Build tests runtime](#2-build-tests-runtime)
    - [3. Run](#3-run)
    - [4. Stop \& clean](#4-stop--clean)
    - [5. Test samples and customization](#5-test-samples-and-customization)
  - [Develop using the SDK](#develop-using-the-sdk)
- [Stack](#stack)
- [Roadmap](#roadmap)
- [References](#references)
- [License](#license)

# Core concepts
The main concept of the Foliage as a high-performance application platform is organizing coordinated work and interaction of a complex heterogeneous software and information systems. The platform's technology is based on the theory of heterogeneous functional graphs.

## Abstraction
The knowledge about a complex/compound system yet stored separately moves into a single associative space. This allows to have transparent knowledge about the entire system and its behavior as one and inseparable whole; gives an ability into account all the hidden relationships and previously unpredictable features; erases the boundary between the system model and the system itself. 
![Alt text](./docs/pics/FoliageUnification.jpg)

By transferring knowledge from different domain planes into a single space, Foliage endows related system components with transparency, consistency, and unambiguity. Reveals weakly detected dependencies, which could be, for example, only inside the head of a devops engineer or in some script. This allows to transparently evaluate the system as a whole and easily endow it with new links and relationships that were previously difficult to implement due to the functional rigidity of the software part.
![Alt text](./docs/pics/FoliageSingleSpace.jpg)

## Features
The full list of features can be found [here.](./docs/features.md)

# Getting Started
```sh
git clone https://github.com/foliagecp/sdk.git
```
For a fully detailed documentation, please visit the  [official docs.](https://pkg.go.dev/github.com/foliagecp/sdk)

## Included tests runtime
### 1. Go to `tests` dir
```
cd tests
```
### 2. Build tests runtime
```sh
docker-compose build
```

### 3. Run
```sh
docker-compose up -d
```
By default the test `basic` sample will be started. To choose another sample to start use:
```
export TEST_NAME=<name> && docker-compose up -d
```

### 4. Stop & clean
```sh
docker-compose down -v
```

### 5. Test samples and customization 
Learn more about existing test samples and their customization to understand the principles of the developing with the Foliage. Here is the list of test samples provided with the SDK:  
- [Basic](./docs/tests/basic.md)

For no-code/low-code statefun logic definition the following plugins are available:
- [JavaScript](./docs/plugins/js.md)

## Develop using the SDK

```sh
git get github.com/foliagecp/sdk
```

1. [Find out how to work with the graph](./docs/graph_crud.md)
2. [Foliage's Json Path Graph Query Language](./docs/jpgql.md)
3. [Write your own application](./docs/how_to_write_an_application.md)
4. [Measure performance](./docs/performance_measures.md)

# Stack
1. Backend
    - Jetstream NATS
    - Key/Value Store NATS
    - WebSocket NATS
    - GoLang
    - JavaScript (V8)
2. Frontend
    - React
    - Typescript/JavaScript
    - WebSocket
3. Common
    - docker
    - docker-compose

[Why NATS?](./docs/technologies_comparison.md)

# Roadmap
![Roadmap](./docs/pics/Roadmap.jpg)

# References
- [Thesaurus](./docs/thesaurus.md)
- [Conventions](./docs/conventions.md)

# License
Unless otherwise noted, the Foliage source files are distributed
under the Apache Version 2.0 license found in the LICENSE file.






