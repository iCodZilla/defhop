package samples.deps;

import com.example.LibDemo;

/** Fixture: F12 into dependency sources (libdemo-sources.jar under .defhop-deps). */
public class DepsUse {
    public void run() {
        LibDemo demo = new LibDemo();
        demo.hello();
    }
}
