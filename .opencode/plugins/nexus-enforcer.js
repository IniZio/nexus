"use strict";
var __createBinding = (this && this.__createBinding) || (Object.create ? (function(o, m, k, k2) {
    if (k2 === undefined) k2 = k;
    var desc = Object.getOwnPropertyDescriptor(m, k);
    if (!desc || ("get" in desc ? !m.__esModule : desc.writable || desc.configurable)) {
      desc = { enumerable: true, get: function() { return m[k]; } };
    }
    Object.defineProperty(o, k2, desc);
}) : (function(o, m, k, k2) {
    if (k2 === undefined) k2 = k;
    o[k2] = m[k];
}));
var __setModuleDefault = (this && this.__setModuleDefault) || (Object.create ? (function(o, v) {
    Object.defineProperty(o, "default", { enumerable: true, value: v });
}) : function(o, v) {
    o["default"] = v;
});
var __importStar = (this && this.__importStar) || (function () {
    var ownKeys = function(o) {
        ownKeys = Object.getOwnPropertyNames || function (o) {
            var ar = [];
            for (var k in o) if (Object.prototype.hasOwnProperty.call(o, k)) ar[ar.length] = k;
            return ar;
        };
        return ownKeys(o);
    };
    return function (mod) {
        if (mod && mod.__esModule) return mod;
        var result = {};
        if (mod != null) for (var k = ownKeys(mod), i = 0; i < k.length; i++) if (k[i] !== "default") __createBinding(result, mod, k[i]);
        __setModuleDefault(result, mod);
        return result;
    };
})();
Object.defineProperty(exports, "__esModule", { value: true });
exports.types = void 0;
exports.createOpenCodePlugin = createOpenCodePlugin;
const engine_1 = require("nexus-enforcer/engine");
const prompts_1 = require("nexus-enforcer/prompts");
const types = __importStar(require("nexus-enforcer/types"));
exports.types = types;
function createOpenCodePlugin(configPath, overridesPath) {
    const engine = (0, engine_1.createValidationEngine)(configPath, overridesPath);
    const generator = (0, prompts_1.createPromptGenerator)();
    let enabled = true;
    let strictMode = false;
    return {
        async validateBefore(context) {
            if (!enabled) {
                return {
                    passed: true,
                    checks: [],
                    overallScore: 100,
                    recommendations: [],
                    executionTime: 0,
                };
            }
            const fullContext = {
                workspacePath: context.workspacePath || process.cwd(),
                workingDirectory: context.workingDirectory || process.cwd(),
                currentFile: context.currentFile,
                currentFunction: context.currentFunction,
                agentType: 'opencode',
                taskDescription: context.taskDescription || '',
                timestamp: new Date(),
                environment: context.environment || {},
            };
            const prompt = generator.generatePrompt('before', fullContext, {
                rules: engine.getEffectiveConfig().rules,
            });
            console.log(prompt);
            const result = engine.validate(fullContext);
            return result;
        },
        async validateAfter(context) {
            if (!enabled) {
                return {
                    passed: true,
                    checks: [],
                    overallScore: 100,
                    recommendations: [],
                    executionTime: 0,
                };
            }
            const fullContext = {
                workspacePath: context.workspacePath || process.cwd(),
                workingDirectory: context.workingDirectory || process.cwd(),
                currentFile: context.currentFile,
                currentFunction: context.currentFunction,
                agentType: 'opencode',
                taskDescription: context.taskDescription || '',
                timestamp: new Date(),
                environment: context.environment || {},
            };
            const result = engine.validate(fullContext);
            const prompt = generator.generatePrompt('after', fullContext, { result });
            console.log(prompt);
            return result;
        },
        getStatus() {
            return {
                enabled,
                strictMode,
                config: engine.getEffectiveConfig(),
            };
        },
        setEnabled(value) {
            enabled = value;
        },
        setStrictMode(value) {
            strictMode = value;
        },
    };
}
//# sourceMappingURL=index.js.map