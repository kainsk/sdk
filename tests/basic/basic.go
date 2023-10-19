// Copyright 2023 NJWS Inc.

// Foliage basic test package.
// Provides the basic example of usage of the SDK.
package basic

import (
	"fmt"
	"os"

	"github.com/foliagecp/easyjson"

	graphCRUD "github.com/foliagecp/sdk/embedded/graph/crud"
	// Comment out and no not use graphDebug for resolving the cgo conflict between go-graphviz and rogchap (when --ldflags '-extldflags "-Wl,--allow-multiple-definition"' does not help)
	graphDebug "github.com/foliagecp/sdk/embedded/graph/debug"
	"github.com/foliagecp/sdk/embedded/graph/jpgql"
	statefun "github.com/foliagecp/sdk/statefun"
	"github.com/foliagecp/sdk/statefun/cache"
	"github.com/foliagecp/sdk/statefun/plugins"
	sfPlugins "github.com/foliagecp/sdk/statefun/plugins"
	sfPluginJS "github.com/foliagecp/sdk/statefun/plugins/js"
	"github.com/foliagecp/sdk/statefun/system"
)

var (
	// NatsURL - nats server url
	NatsURL string = system.GetEnvMustProceed("NATS_URL", "nats://nats:foliage@nats:4222")
	// MasterFunctionContextIncrement - does the master stateful function do the increment operation on each call in its context
	MasterFunctionContextIncrement bool = system.GetEnvMustProceed("MASTER_FUNC_CONTEXT_INCREMENT", true)
	// MasterFunctionContextIncrementOption - Default increment value
	MasterFunctionContextIncrementOption int = system.GetEnvMustProceed("MASTER_FUNC_CONTEXT_INCREMENT_OPTION", 1)
	// MasterFunctionObjectContextProcess - make master function read and write its object context in idle mode
	MasterFunctionObjectContextProcess bool = system.GetEnvMustProceed("MASTER_FUNC_OBJECT_CONTEXT_PROCESS", false)
	// MasterFunctionJSPlugin - enable js plugin for the master function
	MasterFunctionJSPlugin bool = system.GetEnvMustProceed("MASTER_FUNC_JS_PLUGIN", false)
	// MasterFunctionLogs - enable logging of the master function
	MasterFunctionLogs bool = system.GetEnvMustProceed("MASTER_FUNC_LOGS", true)
	// CreateSimpleGraphTest - create a simple graph on runtime start
	CreateSimpleGraphTest bool = system.GetEnvMustProceed("CREATE_SIMPLE_GRAPH_TEST", true)
	// KVMuticesTest - test the Foliage global key/value mutices
	KVMuticesTest bool = system.GetEnvMustProceed("KV_MUTICES_TEST", true)
	// KVMuticesTestDurationSec - key/value mutices test duration
	KVMuticesTestDurationSec int = system.GetEnvMustProceed("KV_MUTICES_TEST_DURATION_SEC", 10)
	// KVMuticesTestWorkers - key/value mutices workers to apply in the test
	KVMuticesTestWorkers int = system.GetEnvMustProceed("KV_MUTICES_TEST_WORKERS", 4)
)

func MasterFunction(executor sfPlugins.StatefunExecutor, contextProcessor *sfPlugins.StatefunContextProcessor) {
	var functionContext *easyjson.JSON
	if MasterFunctionContextIncrement {
		functionContext = contextProcessor.GetFunctionContext()
	}

	options := contextProcessor.Options
	increment := int(options.GetByPath("increment").AsNumericDefault(0))

	if MasterFunctionLogs {
		fmt.Printf("-------> %s:%s\n", contextProcessor.Self.Typename, contextProcessor.Self.ID)
		fmt.Println("== Payload:", contextProcessor.Payload.ToString())
		fmt.Println("== Context:", functionContext.ToString())
	}

	var objectContext *easyjson.JSON
	if MasterFunctionObjectContextProcess {
		objectContext = contextProcessor.GetObjectContext()
		if MasterFunctionLogs {
			fmt.Println("== Object context:", objectContext.ToString())
		}
	}

	if MasterFunctionJSPlugin {
		if executor != nil {
			if err := executor.BuildError(); err != nil {
				fmt.Println(err)
			} else {
				if err := executor.Run(contextProcessor); err != nil {
					fmt.Println(err)

				}
			}
		}
		functionContext = contextProcessor.GetFunctionContext()
	}

	incrementValue := 0
	if MasterFunctionContextIncrement {
		if v, ok := functionContext.GetByPath("counter").AsNumeric(); ok {
			incrementValue = int(v)
		}
		incrementValue += increment
		functionContext.SetByPath("counter", easyjson.NewJSON(incrementValue))
		fmt.Printf("++ Function context's counter value incrementated by %d\n", increment)
	}

	if MasterFunctionObjectContextProcess {
		contextProcessor.SetObjectContext(objectContext)
	}

	if MasterFunctionContextIncrement {
		contextProcessor.SetFunctionContext(functionContext)
	}

	if contextProcessor.RequestReplyData != nil { // Request call is being made
		contextProcessor.RequestReplyData = easyjson.NewJSONObjectWithKeyValue("counter", easyjson.NewJSON(incrementValue)).GetPtr()
	}
}

func RegisterFunctionTypes(runtime *statefun.Runtime) {
	// Create new typename function "functions.tests.basic.master" each stateful instance of which uses go function "MasterFunction"
	ftOptions := easyjson.NewJSONObjectWithKeyValue("increment", easyjson.NewJSON(MasterFunctionContextIncrementOption))
	ft := statefun.NewFunctionType(runtime, "functions.tests.basic.master", MasterFunction, *statefun.NewFunctionTypeConfig().SetOptions(&ftOptions).SetServiceState(true))
	// Add TypenameExecutorPlugin which will provide StatefunExecutor for each stateful instance for this typename function (skip this if TypenameExecutorPlugin is not needed)

	if MasterFunctionJSPlugin {
		jsFileName := "master_function_plugin.js"
		if content, err := os.ReadFile(jsFileName); err == nil {
			// Assign JavaScript StatefunExecutor for TypenameExecutorPlugin
			system.MsgOnErrorReturn(ft.SetExecutor(jsFileName, string(content), sfPluginJS.StatefunExecutorPluginJSContructor))
		} else {
			fmt.Printf("ERROR: Could not load JS script: %v\n", err)
		}
	}

	graphCRUD.RegisterAllFunctionTypes(runtime)
	graphDebug.RegisterAllFunctionTypes(runtime)
	jpgql.RegisterAllFunctionTypes(runtime, 30)
}

func RequestReplyTest(runtime *statefun.Runtime) {
	fmt.Println(">>> Test started: request reply calls")

	funcTypename := "functions.tests.basic.master"
	replyJson, err := runtime.Request(plugins.GolangLocalRequest, funcTypename, "synctest", easyjson.NewJSONObject().GetPtr(), nil)
	if err != nil {
		system.MsgOnErrorReturn(err)
	} else {
		if _, ok := replyJson.GetByPath("counter").AsNumeric(); ok {
			fmt.Printf("GolangLocalRequest test passed! Got reply from %s: %s\n", funcTypename, replyJson.ToString())
		}
	}

	replyJson, err = runtime.Request(plugins.NatsCoreGlobalRequest, funcTypename, "synctest", easyjson.NewJSONObject().GetPtr(), nil)
	if err != nil {
		system.MsgOnErrorReturn(err)
	} else {
		if _, ok := replyJson.GetByPath("counter").AsNumeric(); ok {
			fmt.Printf("NatsCoreGlobalRequest test passed! Got reply from %s: %s\n", funcTypename, replyJson.ToString())
		}
	}

	fmt.Println("<<< Test ended: request reply calls")
}

func Start() {
	afterStart := func(runtime *statefun.Runtime) error {
		RequestReplyTest(runtime)
		if CreateSimpleGraphTest {
			CreateTestGraph(runtime)
		}
		return nil
	}

	if runtime, err := statefun.NewRuntime(*statefun.NewRuntimeConfigSimple(NatsURL, "basic")); err == nil {
		if KVMuticesTest {
			KVMuticesSimpleTest(runtime, KVMuticesTestDurationSec, KVMuticesTestWorkers, 2, 1)
		}

		RegisterFunctionTypes(runtime)
		if err := runtime.Start(cache.NewCacheConfig(), afterStart); err != nil {
			fmt.Printf("Cannot start due to an error: %s\n", err)
		}
	} else {
		fmt.Printf("Cannot create statefun runtime due to an error: %s\n", err)
	}
}

// --------------------------------------------------------------------------------------
