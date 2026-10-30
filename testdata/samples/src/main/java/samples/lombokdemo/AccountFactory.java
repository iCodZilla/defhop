package samples.lombokdemo;

/** Call sites for Lombok-synthesized constructors and SuperBuilder. */
public class AccountFactory {
    public Account empty() {
        return new Account();
    }

    public Account required(String id, String code) {
        return new Account(id, code);
    }

    public Account all(String id, String code, String name) {
        return new Account(id, code, name);
    }

    public Account viaBuilder(String id, String code, String name) {
        return Account.builder().id(id).code(code).name(name).build();
    }
}
