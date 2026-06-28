// IUserService.aidl
package kr.scin.rishmcp;

interface IUserService {
    // Called by the Shizuku server to stop the service process. Keep this id.
    void destroy() = 16777114;

    // Run `sh -c <cmd>` inside the Shizuku-spawned process (uid 2000, shell).
    // Returns a JSON string: {code,stdout,stderr,truncated,durationMs}
    String exec(String cmd, long timeoutMs) = 1;
}
