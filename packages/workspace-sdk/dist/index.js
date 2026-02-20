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
var __exportStar = (this && this.__exportStar) || function(m, exports) {
    for (var p in m) if (p !== "default" && !Object.prototype.hasOwnProperty.call(exports, p)) __createBinding(exports, m, p);
};
Object.defineProperty(exports, "__esModule", { value: true });
exports.ExecOperations = exports.FSOperations = exports.WorkspaceClient = void 0;
var client_1 = require("./client");
Object.defineProperty(exports, "WorkspaceClient", { enumerable: true, get: function () { return client_1.WorkspaceClient; } });
var fs_1 = require("./fs");
Object.defineProperty(exports, "FSOperations", { enumerable: true, get: function () { return fs_1.FSOperations; } });
var exec_1 = require("./exec");
Object.defineProperty(exports, "ExecOperations", { enumerable: true, get: function () { return exec_1.ExecOperations; } });
__exportStar(require("./types"), exports);
//# sourceMappingURL=index.js.map